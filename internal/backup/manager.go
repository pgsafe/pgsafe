package backup

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pgsafe/pgsafe/internal/config"
	"github.com/pgsafe/pgsafe/internal/notify"
)

// Result holds the outcome of a single database backup.
type Result struct {
	ConnName     string
	DBName       string
	SanitizedURL string // password-free "user@host:port/db" for webhook payload
	S3Key        string
	SizeBytes    int64
	Duration     time.Duration
	Err          error
}

// Manager orchestrates backup of all configured databases.
type Manager struct {
	cfg      *config.Config
	notifier *notify.Client
	uploader *s3Uploader
}

func NewManager(cfg *config.Config, notifier *notify.Client) *Manager {
	return &Manager{cfg: cfg, notifier: notifier}
}

// Run executes all backups sequentially and returns a non-nil error if any backup failed.
// A failure in one backup does not prevent subsequent backups from running.
func (m *Manager) Run(ctx context.Context) error {
	up, err := newS3Uploader(ctx, m.cfg)
	if err != nil {
		return fmt.Errorf("init S3 uploader: %w", err)
	}
	m.uploader = up

	startedAt := time.Now()
	slog.Info("backup run starting",
		"multi_connections", len(m.cfg.MultiDatabases),
		"single_connections", len(m.cfg.SingleDatabases),
		"format", m.cfg.DumpFormat,
		"compression", m.cfg.CompressionMethod,
		"encryption", m.cfg.EncryptionCipherKey != "",
	)

	var results []Result

	for _, entry := range m.cfg.MultiDatabases {
		results = append(results, m.backupMulti(ctx, entry)...)
	}

	for _, entry := range m.cfg.SingleDatabases {
		results = append(results, m.backupSingle(ctx, entry))
	}

	return m.summarise(startedAt, results)
}

func (m *Manager) backupMulti(ctx context.Context, entry config.DatabaseEntry) []Result {
	slog.Info("listing databases", "conn", entry.ConnName)

	dbNames, err := listDatabases(ctx, entry.URL)
	if err != nil {
		slog.Error("failed to list databases", "conn", entry.ConnName, "error", err)
		return []Result{{
			ConnName: entry.ConnName,
			DBName:   "(list)",
			Err:      fmt.Errorf("list databases for %s: %w", entry.ConnName, err),
		}}
	}

	slog.Info("discovered databases", "conn", entry.ConnName, "count", len(dbNames))

	if entry.DatabasesGlob != "" {
		dbNames = filterByGlob(dbNames, entry.DatabasesGlob)
		slog.Info("databases after glob filter",
			"conn", entry.ConnName, "glob", entry.DatabasesGlob, "count", len(dbNames))
	}

	if len(dbNames) == 0 {
		slog.Warn("no databases found", "conn", entry.ConnName)
		return nil
	}

	var results []Result
	for _, dbName := range dbNames {
		dbURL, err := config.SubstituteDBName(entry.URL, dbName)
		if err != nil {
			slog.Error("failed to build database URL", "conn", entry.ConnName, "db", dbName, "error", err)
			results = append(results, Result{
				ConnName: entry.ConnName,
				DBName:   dbName,
				Err:      fmt.Errorf("build URL for %s/%s: %w", entry.ConnName, dbName, err),
			})
			continue
		}
		results = append(results, m.backupDatabase(ctx, entry.ConnName, dbName, dbURL))
	}
	return results
}

func (m *Manager) backupSingle(ctx context.Context, entry config.DatabaseEntry) Result {
	dbName, err := config.ExtractDBName(entry.URL)
	if err != nil {
		slog.Error("failed to extract database name", "conn", entry.ConnName, "error", err)
		return Result{
			ConnName: entry.ConnName,
			DBName:   "(unknown)",
			Err:      fmt.Errorf("extract db name from %s: %w", entry.ConnName, err),
		}
	}
	return m.backupDatabase(ctx, entry.ConnName, dbName, entry.URL)
}

func (m *Manager) backupDatabase(ctx context.Context, connName, dbName, dbURL string) Result {
	start := time.Now()
	result := Result{ConnName: connName, DBName: dbName, SanitizedURL: sanitizeDBURL(dbURL)}

	slog.Info("starting backup", "conn", connName, "db", dbName, "format", m.cfg.DumpFormat)

	timestamp := start.UTC().Format("20060102_150405")
	base := fmt.Sprintf("%s_%s_%s", connName, dbName, timestamp)

	pipeline, s3Base, cleanup, err := m.openBackupPipeline(ctx, base, connName, dbName, dbURL)
	if err != nil {
		slog.Error("backup pipeline setup failed", "conn", connName, "db", dbName, "error", err)
		result.Err = err
		return result
	}
	defer cleanup()

	s3Key := s3Base + pipeline.s3Ext
	result.S3Key = s3Key

	slog.Debug("uploading backup", "conn", connName, "db", dbName, "s3_key", s3Key)

	n, uploadErr := m.uploader.upload(ctx, pipeline, s3Key)
	result.SizeBytes = n

	// Always close the pipeline — this is safe to call even if upload failed.
	// Close() first signals upstream processes to stop (via broken pipe), then
	// collects their exit statuses. This prevents deadlocks.
	pipeErr := pipeline.Close()

	switch {
	case uploadErr != nil && pipeErr != nil:
		// Both failed. Log pipe error at debug since it's likely a broken-pipe
		// consequence of the upload failure, not an independent root cause.
		slog.Debug("pipeline close error after upload failure",
			"conn", connName, "db", dbName, "pipe_error", pipeErr)
		result.Err = uploadErr
	case uploadErr != nil:
		result.Err = uploadErr
	case pipeErr != nil:
		result.Err = fmt.Errorf("pipeline close: %w", pipeErr)
	}

	if result.Err != nil {
		slog.Error("backup failed", "conn", connName, "db", dbName, "error", result.Err)
		return result
	}

	result.Duration = time.Since(start)
	slog.Info("backup complete",
		"conn", connName,
		"db", dbName,
		"s3_key", s3Key,
		"bytes", n,
		"duration", result.Duration.Round(time.Second),
	)
	return result
}

// openBackupPipeline starts pg_dump and builds a streaming pipeline ready for
// S3 upload. It returns the pipeline, the base S3 key (without suffix), and a
// cleanup function that must always be deferred by the caller.
//
//   - plain:  pg_dump stdout → pipeline (no disk I/O)
//   - custom: pg_dump → temp file → pipeline (only the raw dump touches disk)
func (m *Manager) openBackupPipeline(ctx context.Context, base, connName, dbName, dbURL string) (*Pipeline, string, func(), error) {
	noop := func() {}

	switch m.cfg.DumpFormat {
	case "plain":
		r, wait, err := runDumpStream(ctx, dbURL, connName, dbName)
		if err != nil {
			return nil, "", noop, fmt.Errorf("pg_dump (plain): %w", err)
		}
		p, err := wrapPipeline(ctx, r, wait, m.cfg.CompressionMethod, m.cfg.EncryptionCipherKey, m.cfg.EncryptionIterations)
		if err != nil {
			// wrapPipeline already called p.Close() which drains pg_dump; noop is safe.
			return nil, "", noop, err
		}
		return p, base + ".sql", noop, nil

	default: // "custom"
		if err := os.MkdirAll(m.cfg.DumpTempDir, 0750); err != nil {
			return nil, "", noop, fmt.Errorf("create temp dir %s: %w", m.cfg.DumpTempDir, err)
		}
		dumpPath := filepath.Join(m.cfg.DumpTempDir, base+".dump")

		// cleanup removes the temp file regardless of what happens next.
		cleanup := func() {
			if err := os.Remove(dumpPath); err != nil && !os.IsNotExist(err) {
				slog.Warn("failed to remove dump file", "path", dumpPath, "error", err)
			}
		}

		if err := runDump(ctx, dbURL, connName, dbName, dumpPath); err != nil {
			// Dump failed; clean up whatever was written.
			cleanup()
			return nil, "", noop, fmt.Errorf("pg_dump (custom): %w", err)
		}

		p, err := openPipeline(ctx, dumpPath, m.cfg.CompressionMethod, m.cfg.EncryptionCipherKey, m.cfg.EncryptionIterations)
		if err != nil {
			cleanup()
			return nil, "", noop, fmt.Errorf("open pipeline: %w", err)
		}

		return p, base + ".dump", cleanup, nil
	}
}

func (m *Manager) summarise(startedAt time.Time, results []Result) error {
	finishedAt := time.Now()

	var failed []string
	var succeeded int
	for _, r := range results {
		if r.Err != nil {
			failed = append(failed, fmt.Sprintf("%s/%s: %v", r.ConnName, r.DBName, r.Err))
		} else {
			succeeded++
		}
	}

	slog.Info("backup run finished",
		"total", len(results),
		"succeeded", succeeded,
		"failed", len(failed),
	)

	if len(failed) > 0 {
		slog.Error("backup run failed", "failures", failed)
		m.notifier.NotifyFailure(startedAt, finishedAt, failed)
		return fmt.Errorf("backup run finished with errors (%d/%d succeeded)", succeeded, len(results))
	}

	var dbURLs []string
	for _, r := range results {
		if r.SanitizedURL != "" {
			dbURLs = append(dbURLs, r.SanitizedURL)
		}
	}
	m.notifier.NotifySuccess(startedAt, finishedAt, dbURLs)
	return nil
}

// sanitizeDBURL strips the scheme and password from a PostgreSQL URL, returning
// "user@host:port/db" (or "host:port/db" when there is no username).
func sanitizeDBURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := u.Host
	db := strings.TrimPrefix(u.Path, "/")
	if u.User != nil && u.User.Username() != "" {
		return u.User.Username() + "@" + host + "/" + db
	}
	return host + "/" + db
}

func filterByGlob(names []string, pattern string) []string {
	var out []string
	for _, name := range names {
		if matched, _ := filepath.Match(pattern, name); matched {
			out = append(out, name)
		}
	}
	return out
}

// listDatabases queries pg_database for all non-template databases.
func listDatabases(ctx context.Context, dbURL string) ([]string, error) {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)

	rows, err := conn.Query(ctx, `
		SELECT datname
		FROM pg_database
		WHERE datistemplate = false
		ORDER BY datname
	`)
	if err != nil {
		return nil, fmt.Errorf("query pg_database: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return names, nil
}
