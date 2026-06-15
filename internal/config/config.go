package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/viper"
)

var connNameRe = regexp.MustCompile(`^[A-Z0-9]+$`)

// ValidationError is returned by Load when one or more env vars are invalid.
// Callers can extract the individual messages for structured logging.
type ValidationError struct {
	Details []string
}

func (e *ValidationError) Error() string {
	return "configuration errors: " + strings.Join(e.Details, "; ")
}

type DatabaseEntry struct {
	ConnName      string
	URL           string
	DatabasesGlob string // MULTI only; empty means back up all databases
}

type Config struct {
	MultiDatabases  []DatabaseEntry
	SingleDatabases []DatabaseEntry

	S3Endpoint           string
	S3Bucket             string
	S3Region             string
	S3AccessKeyID        string
	S3SecretAccessKey    string
	S3PathStyle          bool
	S3MultipartPartSizeMB int // minimum 5

	DumpFormat             string // "plain", "custom", or "tar"
	DumpJobs               int    // parallel dump workers; only used for tar format
	CompressionMethod      string
	EncryptionCipherKey    string
	EncryptionIterations   int

	SlackWebhookURL string

	WebhookSuccessURL string
	WebhookFailureURL string

	SMTPHost      string
	SMTPPort      int
	SMTPTLSMode   string
	SMTPUsername  string
	SMTPPassword  string
	SMTPFromName  string
	SMTPFromEmail string
	SMTPToEmail   string
}

func Load() (*Config, error) {
	v := viper.New()
	v.AutomaticEnv()
	v.SetDefault("DUMP_FORMAT", "custom")
	v.SetDefault("DUMP_JOBS", 1)
	v.SetDefault("COMPRESSION_METHOD", "none")
	v.SetDefault("S3_PATH_STYLE", false)
	v.SetDefault("S3_MULTIPART_PART_SIZE", 64)
	v.SetDefault("ENCRYPTION_ITERATIONS", 100000)
	v.SetDefault("SMTP_TLS_MODE", "starttls")
	v.SetDefault("SMTP_PORT", 587)

	var errs []string

	s3Endpoint := v.GetString("S3_ENDPOINT")
	s3Bucket := v.GetString("S3_BUCKET")
	s3Region := v.GetString("S3_REGION")
	s3AccessKeyID := v.GetString("S3_ACCESS_KEY_ID")
	s3SecretAccessKey := v.GetString("S3_SECRET_ACCESS_KEY")

	if s3Endpoint == "" {
		errs = append(errs, "S3_ENDPOINT is required")
	}
	if s3Bucket == "" {
		errs = append(errs, "S3_BUCKET is required")
	}
	if s3Region == "" {
		errs = append(errs, "S3_REGION is required")
	}
	if s3AccessKeyID == "" {
		errs = append(errs, "S3_ACCESS_KEY_ID is required")
	}
	if s3SecretAccessKey == "" {
		errs = append(errs, "S3_SECRET_ACCESS_KEY is required")
	}

	s3MultipartPartSizeMB := v.GetInt("S3_MULTIPART_PART_SIZE")
	if s3MultipartPartSizeMB < 5 {
		errs = append(errs, fmt.Sprintf("S3_MULTIPART_PART_SIZE must be >= 5 MB (got %d)", s3MultipartPartSizeMB))
	}

	dumpFormat := v.GetString("DUMP_FORMAT")
	switch dumpFormat {
	case "plain", "custom", "tar":
	default:
		errs = append(errs, fmt.Sprintf("DUMP_FORMAT must be 'plain', 'custom', or 'tar' (got %q)", dumpFormat))
	}

	dumpJobs := v.GetInt("DUMP_JOBS")
	if dumpJobs < 1 {
		errs = append(errs, fmt.Sprintf("DUMP_JOBS must be >= 1 (got %d)", dumpJobs))
	}

	compressionMethod := v.GetString("COMPRESSION_METHOD")
	switch compressionMethod {
	case "none", "gzip", "bzip2", "xz":
	default:
		errs = append(errs, fmt.Sprintf("COMPRESSION_METHOD must be one of: none, gzip, bzip2, xz (got %q)", compressionMethod))
	}

	encryptionIterations := v.GetInt("ENCRYPTION_ITERATIONS")
	if encryptionIterations <= 0 {
		errs = append(errs, fmt.Sprintf("ENCRYPTION_ITERATIONS must be a positive integer (got %d)", encryptionIterations))
	}

	smtpHost := v.GetString("SMTP_HOST")
	smtpPort := v.GetInt("SMTP_PORT")
	smtpTLSMode := v.GetString("SMTP_TLS_MODE")
	smtpFromEmail := v.GetString("SMTP_FROM_EMAIL")
	smtpToEmail := v.GetString("SMTP_TO_EMAIL")

	if smtpHost != "" {
		if smtpPort <= 0 {
			errs = append(errs, fmt.Sprintf("SMTP_PORT must be a positive integer (got %d)", smtpPort))
		}
		switch smtpTLSMode {
		case "none", "starttls", "tls":
		default:
			errs = append(errs, fmt.Sprintf("SMTP_TLS_MODE must be one of: none, starttls, tls (got %q)", smtpTLSMode))
		}
		if smtpFromEmail == "" {
			errs = append(errs, "SMTP_FROM_EMAIL is required when SMTP_HOST is set")
		}
		if smtpToEmail == "" {
			errs = append(errs, "SMTP_TO_EMAIL is required when SMTP_HOST is set")
		}
	}

	multiDBs, singleDBs, dbErrs := parseDatabaseURLs()
	errs = append(errs, dbErrs...)

	if len(multiDBs) == 0 && len(singleDBs) == 0 && len(dbErrs) == 0 {
		errs = append(errs, "no database URLs configured; set at least one MULTI_*_DATABASE_URL or SINGLE_*_DATABASE_URL")
	}

	for _, entry := range singleDBs {
		if _, err := ExtractDBName(entry.URL); err != nil {
			errs = append(errs, fmt.Sprintf("SINGLE_%s_DATABASE_URL: cannot extract database name: %v", entry.ConnName, err))
		}
	}

	if len(errs) > 0 {
		return nil, &ValidationError{Details: errs}
	}

	return &Config{
		MultiDatabases:      multiDBs,
		SingleDatabases:     singleDBs,
		S3Endpoint:          s3Endpoint,
		S3Bucket:            s3Bucket,
		S3Region:            s3Region,
		S3AccessKeyID:       s3AccessKeyID,
		S3SecretAccessKey:   s3SecretAccessKey,
		S3PathStyle:           v.GetBool("S3_PATH_STYLE"),
		S3MultipartPartSizeMB: s3MultipartPartSizeMB,
		DumpFormat:          dumpFormat,
		DumpJobs:            dumpJobs,
		CompressionMethod:   compressionMethod,
		EncryptionCipherKey:  v.GetString("ENCRYPTION_CIPHER_KEY"),
		EncryptionIterations: encryptionIterations,
		WebhookSuccessURL: v.GetString("WEBHOOK_SUCCESS_URL"),
		WebhookFailureURL: v.GetString("WEBHOOK_FAILURE_URL"),

		SlackWebhookURL: v.GetString("SLACK_WEBHOOK_URL"),

		SMTPHost:      smtpHost,
		SMTPPort:      smtpPort,
		SMTPTLSMode:   smtpTLSMode,
		SMTPUsername:  v.GetString("SMTP_USERNAME"),
		SMTPPassword:  v.GetString("SMTP_PASSWORD"),
		SMTPFromName:  v.GetString("SMTP_FROM_NAME"),
		SMTPFromEmail: smtpFromEmail,
		SMTPToEmail:   smtpToEmail,
	}, nil
}

func parseDatabaseURLs() (multi, single []DatabaseEntry, errs []string) {
	globs := map[string]string{} // connName → glob pattern

	for _, env := range os.Environ() {
		k, v, ok := strings.Cut(env, "=")
		if !ok {
			continue
		}

		if strings.HasPrefix(k, "MULTI_") && strings.HasSuffix(k, "_DATABASE_NAMES_GLOB") {
			connName := strings.TrimSuffix(strings.TrimPrefix(k, "MULTI_"), "_DATABASE_NAMES_GLOB")
			if !connNameRe.MatchString(connName) {
				errs = append(errs, fmt.Sprintf("env var %s: CONNNAME %q must match [A-Z0-9]+", k, connName))
				continue
			}
			if _, err := filepath.Match(v, ""); err != nil {
				errs = append(errs, fmt.Sprintf("env var %s: invalid glob pattern %q: %v", k, v, err))
				continue
			}
			globs[connName] = v
			continue
		}

		var prefix, suffix string
		if strings.HasPrefix(k, "MULTI_") && strings.HasSuffix(k, "_DATABASE_URL") {
			prefix, suffix = "MULTI_", "_DATABASE_URL"
		} else if strings.HasPrefix(k, "SINGLE_") && strings.HasSuffix(k, "_DATABASE_URL") {
			prefix, suffix = "SINGLE_", "_DATABASE_URL"
		} else {
			continue
		}

		connName := strings.TrimSuffix(strings.TrimPrefix(k, prefix), suffix)
		if !connNameRe.MatchString(connName) {
			errs = append(errs, fmt.Sprintf("env var %s: CONNNAME %q must match [A-Z0-9]+", k, connName))
			continue
		}
		if v == "" {
			errs = append(errs, fmt.Sprintf("env var %s: URL must not be empty", k))
			continue
		}

		entry := DatabaseEntry{ConnName: connName, URL: v}
		if prefix == "MULTI_" {
			multi = append(multi, entry)
		} else {
			single = append(single, entry)
		}
	}

	for i := range multi {
		multi[i].DatabasesGlob = globs[multi[i].ConnName]
	}

	sort.Slice(multi, func(i, j int) bool { return multi[i].ConnName < multi[j].ConnName })
	sort.Slice(single, func(i, j int) bool { return single[i].ConnName < single[j].ConnName })
	return
}

// ExtractDBName parses the database name from a PostgreSQL connection URL.
func ExtractDBName(dbURL string) (string, error) {
	u, err := url.Parse(dbURL)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" {
		return "", fmt.Errorf("URL has no database name in path")
	}
	return name, nil
}

// SubstituteDBName returns a copy of dbURL with the database path component replaced.
func SubstituteDBName(dbURL, dbName string) (string, error) {
	u, err := url.Parse(dbURL)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	u.Path = "/" + dbName
	return u.String(), nil
}
