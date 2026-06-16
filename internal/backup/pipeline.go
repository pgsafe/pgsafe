package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
)

// Pipeline is a streaming reader chain that optionally compresses and/or
// encrypts data before it reaches the S3 upload manager.
//
// Resilience design:
//   - Close() closes the final reader first, which sends EPIPE/ErrClosedPipe
//     upstream through the process chain, allowing all subprocesses to exit
//     promptly before Wait() is called. This prevents deadlocks when an
//     upload fails mid-stream.
//   - gzipStream closes its source reader on exit so that a stalled source
//     (e.g. a pg_dump process) is unblocked and can exit cleanly.
//   - All subprocesses are started with CommandContext so they are killed
//     when the parent context is cancelled.
type Pipeline struct {
	reader io.Reader
	waits  []func() error
	s3Ext  string // accumulated suffix, e.g. ".gz.enc"
}

// wrapPipeline wraps an existing ReadCloser (e.g. pg_dump stdout) with the
// compression/encryption chain. waitFn is prepended so it is called last on
// Close, after all downstream processes have already exited.
func wrapPipeline(ctx context.Context, src io.ReadCloser, waitFn func() error, compressionAlgorithm, cipherKey string, compressionJobs, iterations int) (*Pipeline, error) {
	p := &Pipeline{
		reader: src,
		waits:  []func() error{waitFn},
	}
	if err := p.build(ctx, compressionAlgorithm, cipherKey, compressionJobs, iterations); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func (p *Pipeline) build(ctx context.Context, compressionAlgorithm, cipherKey string, compressionJobs, iterations int) error {
	if compressionAlgorithm != "" && compressionAlgorithm != "none" {
		ext, err := p.applyCompression(ctx, compressionAlgorithm, compressionJobs)
		if err != nil {
			return fmt.Errorf("setup compression (%s): %w", compressionAlgorithm, err)
		}
		p.s3Ext += ext
	}
	if cipherKey != "" {
		if err := p.applyEncryption(ctx, cipherKey, iterations); err != nil {
			return fmt.Errorf("setup encryption: %w", err)
		}
		p.s3Ext += ".enc"
	}
	return nil
}

func (p *Pipeline) Read(b []byte) (int, error) { return p.reader.Read(b) }

// Close shuts down the pipeline in a deadlock-safe order:
//  1. Close the final reader – this sends EPIPE to the last subprocess, which
//     cascades backwards through the process chain so every stage exits.
//  2. Call each wait function in reverse order (last stage first) so we collect
//     exit statuses only after the processes have already stopped.
//
// In the happy path all data has been consumed, so step 1 is a no-op on an
// already-EOF reader and every wait returns 0. In the abort path (upload error)
// step 1 unblocks blocked writers quickly, avoiding indefinite hangs.
func (p *Pipeline) Close() error {
	// Step 1: signal upstream to stop.
	if c, ok := p.reader.(io.Closer); ok {
		c.Close() // intentionally ignore error – just unblocking
	}

	// Step 2: collect subprocess exit statuses.
	var errs []error
	for i := len(p.waits) - 1; i >= 0; i-- {
		if err := p.waits[i](); err != nil {
			errs = append(errs, err)
		}
	}

	return joinErrors(errs)
}

func (p *Pipeline) applyCompression(ctx context.Context, algorithm string, jobs int) (string, error) {
	jobs_s := strconv.Itoa(jobs)
	switch algorithm {
	case "gzip":
		var r io.ReadCloser
		var wait func() error
		var err error
		if jobs > 1 {
			r, wait, err = cmdStream(ctx, p.reader, "pigz", "-c", "-p", jobs_s)
		} else {
			r, wait = gzipStream(p.reader)
		}
		if err != nil {
			return "", err
		}
		p.reader = r
		p.waits = append(p.waits, wait)
		return ".gz", nil
	case "bzip2":
		var cmd string
		var args []string
		if jobs > 1 {
			cmd, args = "pbzip2", []string{"-c", "-p" + jobs_s}
		} else {
			cmd, args = "bzip2", []string{"-c"}
		}
		r, wait, err := cmdStream(ctx, p.reader, cmd, args...)
		if err != nil {
			return "", err
		}
		p.reader = r
		p.waits = append(p.waits, wait)
		return ".bz2", nil
	case "xz":
		r, wait, err := cmdStream(ctx, p.reader, "xz", "-T", jobs_s, "-c")
		if err != nil {
			return "", err
		}
		p.reader = r
		p.waits = append(p.waits, wait)
		return ".xz", nil
	default:
		return "", fmt.Errorf("unsupported compression algorithm: %s", algorithm)
	}
}

func (p *Pipeline) applyEncryption(ctx context.Context, cipherKey string, iterations int) error {
	args := []string{
		"enc", "-aes-256-cbc", "-salt", "-pbkdf2", "-iter", strconv.Itoa(iterations),
		"-pass", "env:PGSAFE_CIPHER_KEY",
	}
	cmd := exec.CommandContext(ctx, "openssl", args...)
	cmd.Stdin = p.reader
	cmd.Env = append(os.Environ(), "PGSAFE_CIPHER_KEY="+cipherKey)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("openssl stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start openssl: %w", err)
	}
	p.reader = stdout
	p.waits = append(p.waits, func() error {
		if err := cmd.Wait(); err != nil {
			slog.Debug("openssl stderr", "stderr", stderr.String())
			return fmt.Errorf("openssl: %w", err)
		}
		return nil
	})
	return nil
}

// gzipStream compresses src using Go's stdlib gzip in a background goroutine.
//
// When the goroutine exits (whether normally or due to a broken-pipe abort) it
// closes src so that upstream processes (e.g. pg_dump) are not left blocked
// writing to a reader that has stopped consuming.
func gzipStream(src io.Reader) (io.ReadCloser, func() error) {
	pr, pw := io.Pipe()
	done := make(chan error, 1)

	go func() {
		gw := gzip.NewWriter(pw)
		_, err := io.Copy(gw, src)
		if err == nil {
			err = gw.Close()
		}
		if err != nil {
			pw.CloseWithError(err)
		} else {
			pw.Close()
		}
		// Close src so any blocked upstream writer (e.g. pg_dump stdout) is unblocked.
		if c, ok := src.(io.Closer); ok {
			c.Close()
		}
		done <- err
	}()

	return pr, func() error { return <-done }
}

// cmdStream starts an external command, wiring src to stdin and returning its
// stdout as a ReadCloser. When the subprocess exits its stdin fd is closed by
// the OS, which propagates EPIPE to the previous pipeline stage.
func cmdStream(ctx context.Context, src io.Reader, name string, args ...string) (io.ReadCloser, func() error, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = src
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdout pipe for %s: %w", name, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start %s: %w", name, err)
	}
	wait := func() error {
		if err := cmd.Wait(); err != nil {
			slog.Debug("subprocess stderr", "cmd", name, "stderr", stderr.String())
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}
	return stdout, wait, nil
}

func joinErrors(errs []error) error {
	var nonNil []error
	for _, e := range errs {
		if e != nil {
			nonNil = append(nonNil, e)
		}
	}
	switch len(nonNil) {
	case 0:
		return nil
	case 1:
		return nonNil[0]
	default:
		msg := nonNil[0].Error()
		for _, e := range nonNil[1:] {
			msg += "; " + e.Error()
		}
		return fmt.Errorf("%s", msg)
	}
}
