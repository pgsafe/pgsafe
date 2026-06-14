package notify

import (
	"encoding/json"
	"log/slog"
	"time"
)

type webhookDB struct {
	URL string `json:"url"`
}

func (c *Client) postWebhookSuccess(startedAt, finishedAt time.Time, dbURLs []string) {
	dbs := make([]webhookDB, len(dbURLs))
	for i, u := range dbURLs {
		dbs[i] = webhookDB{URL: u}
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
