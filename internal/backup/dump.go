package backup

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
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

// runDump executes pg_dump writing the custom-format archive to outputPath on disk.
func runDump(ctx context.Context, dbURL, connName, dbName, outputPath string) error {
	args := []string{
		"--format=c",
		"--verbose",
		fmt.Sprintf("--file=%s", outputPath),
		"--no-owner",
		"--no-privileges",
		"--compress=0",
		fmt.Sprintf("--dbname=%s", dbURL),
	}

	cmd := exec.CommandContext(ctx, "pg_dump", args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("pg_dump stderr pipe: %w", err)
	}

	slog.Debug("pg_dump starting", "format", "custom", "conn", connName, "db", dbName, "output", outputPath)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start pg_dump: %w", err)
	}

	done := logLines(stderr, "conn", connName, "db", dbName)
	<-done

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("pg_dump: %w", err)
	}
	return nil
}

// runDumpStream starts pg_dump in plain-SQL mode and returns its stdout as a streaming
// reader. No temporary file is written. The caller must drain the reader fully and then
// call wait() to collect the exit status.
func runDumpStream(ctx context.Context, dbURL, connName, dbName string) (io.ReadCloser, func() error, error) {
	args := []string{
		"--format=plain",
		"--verbose",
		"--no-owner",
		"--no-privileges",
		fmt.Sprintf("--dbname=%s", dbURL),
	}

	cmd := exec.CommandContext(ctx, "pg_dump", args...)

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

	slog.Debug("pg_dump started", "format", "plain/stream", "conn", connName, "db", dbName)

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
