package notify

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// Config groups all notifier settings passed to New.
type Config struct {
	SlackWebhookURL   string
	SMTP              *SMTPConfig
	WebhookSuccessURL string
	WebhookFailureURL string
}

// Client delivers run-outcome notifications (success or failure) to all
// configured channels. All sends are asynchronous, independent of each other,
// and retried up to 10 times with exponential backoff before giving up. Call
// Flush() before process exit to ensure all in-flight notifications are
// delivered.
type Client struct {
	slackURL          string
	smtp              *smtpSender
	webhookSuccessURL string
	webhookFailureURL string
	http              *http.Client
	wg                sync.WaitGroup
}

func New(cfg Config) *Client {
	return &Client{
		slackURL:          cfg.SlackWebhookURL,
		smtp:              newSMTPSender(cfg.SMTP),
		webhookSuccessURL: cfg.WebhookSuccessURL,
		webhookFailureURL: cfg.WebhookFailureURL,
		http:              &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) active() bool {
	return c.slackURL != "" || c.smtp != nil || c.webhookSuccessURL != "" || c.webhookFailureURL != ""
}

// Flush waits for all in-flight notifications to complete or until 5 minutes
// have elapsed (enough for 10 retry attempts across all channels).
func (c *Client) Flush() {
	if !c.active() {
		return
	}
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Minute):
		slog.Warn("notify: timed out waiting for in-flight notifications")
	}
}

// DBInfo describes a single successfully backed-up database.
type DBInfo struct {
	ConnName  string
	DBName    string
	Mode      string // "single" or "multi"
	URL       string // sanitized, no password
	SizeBytes int64
	Duration  time.Duration
}

// NotifySuccess fires all configured channels asynchronously to report a
// successful backup run.
func (c *Client) NotifySuccess(startedAt, finishedAt time.Time, databases []DBInfo) {
	if c.slackURL != "" {
		c.dispatch(func() { c.sendSlackSuccess(startedAt, finishedAt, databases) })
	}
	if c.smtp != nil {
		c.dispatch(func() { c.smtp.sendSuccess(startedAt, finishedAt, databases) })
	}
	if c.webhookSuccessURL != "" {
		c.dispatch(func() { c.postWebhookSuccess(startedAt, finishedAt, databases) })
	}
}

// NotifyFailure fires all configured channels asynchronously to report a
// failed backup run. failures contains one entry per failed database in
// "conn/db: error" form.
func (c *Client) NotifyFailure(startedAt, finishedAt time.Time, failures []string) {
	if c.slackURL != "" {
		c.dispatch(func() { c.sendSlackFailure(startedAt, finishedAt, failures) })
	}
	if c.smtp != nil {
		c.dispatch(func() { c.smtp.sendFailure(startedAt, finishedAt, failures) })
	}
	if c.webhookFailureURL != "" {
		c.dispatch(func() { c.postWebhookFailure(startedAt, finishedAt, failures) })
	}
}

func (c *Client) dispatch(fn func()) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		fn()
	}()
}

// httpPostWithRetry delivers body to url via withRetry, treating network errors
// and non-2xx responses as retryable failures. Used by Slack and webhook channels.
func (c *Client) httpPostWithRetry(kind, url, contentType string, body []byte) {
	withRetry(kind, func() error {
		resp, err := c.http.Post(url, contentType, bytes.NewReader(body))
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil
	})
}
