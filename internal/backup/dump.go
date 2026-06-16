package backup

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
)

// logLines reads lines from r and emits each as a separate slog.Debug record.
// Returns a channel that is closed when the reader reaches EOF or errors.
func logLines(r io.Reader, args ...any) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s := bufio.NewScanner(r)
		for s.Scan() {
			slog.Debug(s.Text(), args...)
		}
	}()
	return done
}

// runDumpStream starts pg_dump and returns its stdout as a streaming reader.
// format must be "plain", "custom", or "tar". For custom, --compress=0 is
// added so that compression is handled by the pipeline layer. For tar, jobs
// controls parallel table dumping (--jobs). The caller must drain the reader
// fully and then call wait() to collect the exit status.
func runDumpStream(ctx context.Context, dbURL, connName, dbName, format string, jobs int, extraArgs []string) (io.ReadCloser, func() error, error) {
	args := []string{
		fmt.Sprintf("--format=%s", format),
		"--verbose",
	}
	if len(extraArgs) == 0 {
		args = append(args, "--no-owner", "--no-privileges")
	}
	switch format {
	case "custom":
		args = append(args, "--compress=0")
	case "tar":
		args = append(args, fmt.Sprintf("--jobs=%d", jobs))
	}
	args = append(args, extraArgs...)
	args = append(args, fmt.Sprintf("--dbname=%s", dbURL))

	cmd := exec.CommandContext(ctx, "pg_dump", args...)

	logArgs := make([]string, len(args))
	copy(logArgs, args)
	for i, a := range logArgs {
		if strings.HasPrefix(a, "--dbname=") {
			logArgs[i] = "--dbname=" + sanitizeDBURL(strings.TrimPrefix(a, "--dbname="))
		}
	}
	slog.Debug("pg_dump command", "conn", connName, "db", dbName, "args", logArgs)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("pg_dump stderr pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("pg_dump stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start pg_dump: %w", err)
	}

	done := logLines(stderr, "conn", connName, "db", dbName)

	wait := func() error {
		<-done
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("pg_dump: %w", err)
		}
		return nil
	}
	return stdout, wait, nil
}
