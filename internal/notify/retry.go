package notify

import (
	"log/slog"
	"time"
)

const (
	retryMaxAttempts = 10
	retryInitBackoff = 2 * time.Second
	retryMaxBackoff  = 30 * time.Second
)

// withRetry calls fn up to retryMaxAttempts times. On each failure it logs a
// warning and sleeps with exponential backoff (2 s initial, 30 s cap). If all
// attempts are exhausted the final error is logged at Error level.
// kind is a short label used in log messages (e.g. "slack", "smtp", "webhook/success").
func withRetry(kind string, fn func() error) {
	backoff := retryInitBackoff
	for attempt := 1; attempt <= retryMaxAttempts; attempt++ {
		if err := fn(); err == nil {
			return
		} else if attempt == retryMaxAttempts {
			slog.Error(kind+": all retry attempts exhausted", "error", err)
			return
		} else {
			slog.Warn(kind+": send failed, retrying",
				"attempt", attempt, "next_in", backoff, "error", err)
			time.Sleep(backoff)
			backoff = min(backoff*2, retryMaxBackoff)
		}
	}
}
