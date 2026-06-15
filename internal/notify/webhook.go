package notify

import (
	"encoding/json"
	"log/slog"
	"time"
)

type webhookDB struct {
	ConnName  string `json:"connName"`
	DBName    string `json:"dbName"`
	Mode      string `json:"mode"`
	URL       string `json:"url"`
	SizeBytes int64  `json:"sizeBytes"`
	Duration  string `json:"duration"`
}

func (c *Client) postWebhookSuccess(startedAt, finishedAt time.Time, databases []DBInfo) {
	dbs := make([]webhookDB, len(databases))
	for i, d := range databases {
		dbs[i] = webhookDB{
			ConnName:  d.ConnName,
			DBName:    d.DBName,
			Mode:      d.Mode,
			URL:       d.URL,
			SizeBytes: d.SizeBytes,
			Duration:  d.Duration.Round(time.Second).String(),
		}
	}
	payload := struct {
		StartedAt  string      `json:"startedAt"`
		FinishedAt string      `json:"finishedAt"`
		Databases  []webhookDB `json:"databases"`
	}{
		StartedAt:  startedAt.UTC().Format(time.RFC3339),
		FinishedAt: finishedAt.UTC().Format(time.RFC3339),
		Databases:  dbs,
	}
	c.postWebhook("webhook/success", c.webhookSuccessURL, payload)
}

func (c *Client) postWebhookFailure(startedAt, finishedAt time.Time, failures []string) {
	payload := struct {
		StartedAt  string   `json:"startedAt"`
		FinishedAt string   `json:"finishedAt"`
		Failures   []string `json:"failures"`
	}{
		StartedAt:  startedAt.UTC().Format(time.RFC3339),
		FinishedAt: finishedAt.UTC().Format(time.RFC3339),
		Failures:   failures,
	}
	c.postWebhook("webhook/failure", c.webhookFailureURL, payload)
}

func (c *Client) postWebhook(kind, url string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("webhook: marshal payload", "kind", kind, "error", err)
		return
	}
	c.httpPostWithRetry(kind, url, "application/json", body)
}
