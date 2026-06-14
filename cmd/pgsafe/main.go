package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/pgsafe/pgsafe/internal/backup"
	"github.com/pgsafe/pgsafe/internal/config"
	"github.com/pgsafe/pgsafe/internal/notify"
	"github.com/pgsafe/pgsafe/internal/telemetry"
)

func main() {
	ctx := context.Background()

	base := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	h, otelShutdown, err := telemetry.Setup(ctx, base)
	if err != nil {
		slog.Error("OpenTelemetry setup failed", "error", err)
		os.Exit(1)
	}
	slog.SetDefault(slog.New(h))
	defer otelShutdown()

	cfg, err := config.Load()
	if err != nil {
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			slog.Error("configuration error", "errors", ve.Details)
		} else {
			slog.Error("configuration error", "error", err)
		}
		os.Exit(1)
	}

	var smtpCfg *notify.SMTPConfig
	if cfg.SMTPHost != "" {
		smtpCfg = &notify.SMTPConfig{
			Host:      cfg.SMTPHost,
			Port:      cfg.SMTPPort,
			TLSMode:   cfg.SMTPTLSMode,
			Username:  cfg.SMTPUsername,
			Password:  cfg.SMTPPassword,
			FromName:  cfg.SMTPFromName,
			FromEmail: cfg.SMTPFromEmail,
			ToEmail:   cfg.SMTPToEmail,
		}
	}
	notifier := notify.New(notify.Config{
		SlackWebhookURL:   cfg.SlackWebhookURL,
		SMTP:              smtpCfg,
		WebhookSuccessURL: cfg.WebhookSuccessURL,
		WebhookFailureURL: cfg.WebhookFailureURL,
	})

	// Flush ensures in-flight notifications are delivered before exit,
	// which is critical in job mode where the process exits immediately after.
	defer notifier.Flush()

	mgr := backup.NewManager(cfg, notifier)
	if err := mgr.Run(ctx); err != nil {
		os.Exit(1)
	}
}
