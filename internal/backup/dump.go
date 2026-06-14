package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
)

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

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "pg_dump", args...)
	cmd.Stderr = &stderr

	slog.Debug("pg_dump starting", "format", "custom", "conn", connName, "db", dbName, "output", outputPath)

	if err := cmd.Run(); err != nil {
		slog.Debug("pg_dump stderr", "stderr", stderr.String())
		return fmt.Errorf("pg_dump: %w", err)
	}
	if stderr.Len() > 0 {
		slog.Debug("pg_dump verbose output", "conn", connName, "db", dbName, "output", stderr.String())
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

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "pg_dump", args...)
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("pg_dump stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start pg_dump: %w", err)
	}

	slog.Debug("pg_dump started", "format", "plain/stream", "conn", connName, "db", dbName)

	wait := func() error {
		if err := cmd.Wait(); err != nil {
			slog.Debug("pg_dump stderr", "stderr", stderr.String())
			return fmt.Errorf("pg_dump: %w", err)
		}
		if stderr.Len() > 0 {
			slog.Debug("pg_dump verbose output", "conn", connName, "db", dbName, "output", stderr.String())
		}
		return nil
	}
	return stdout, wait, nil
}
