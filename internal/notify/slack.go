package notify

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

func (c *Client) sendSlackSuccess(startedAt, finishedAt time.Time, dbURLs []string) {
	var b strings.Builder
	b.WriteString(":white_check_mark: *[pgsafe]* Backup run succeeded\n")
	b.WriteString(fmt.Sprintf("*startedAt:* %s\n", startedAt.UTC().Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("*finishedAt:* %s\n", finishedAt.UTC().Format(time.RFC3339)))
	b.WriteString("*databases:*\n")
	for _, u := range dbURLs {
		b.WriteString("• " + u + "\n")
	}
	c.postSlack(strings.TrimRight(b.String(), "\n"))
}

func (c *Client) sendSlackFailure(startedAt, finishedAt time.Time, failures []string) {
	var b strings.Builder
	b.WriteString(":x: *[pgsafe]* Backup run failed\n")
	b.WriteString(fmt.Sprintf("*startedAt:* %s\n", startedAt.UTC().Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("*finishedAt:* %s\n", finishedAt.UTC().Format(time.RFC3339)))
	b.WriteString("*failures:*\n")
	for _, f := range failures {
		b.WriteString("• " + f + "\n")
	}
	c.postSlack(strings.TrimRight(b.String(), "\n"))
}

func (c *Client) postSlack(text string) {
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		slog.Error("slack: marshal payload", "error", err)
		return
	}
	c.httpPostWithRetry("slack", c.slackURL, "application/json", payload)
}
