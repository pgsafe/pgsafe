package notify

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTPConfig holds SMTP notifier settings. All fields are read at construction
// time; the struct is not used after New() returns.
type SMTPConfig struct {
	Host      string
	Port      int
	TLSMode   string // "none" | "starttls" | "tls"
	Username  string
	Password  string
	FromName  string
	FromEmail string
	ToEmail   string
}

// smtpSender is the runtime SMTP state held inside Client.
type smtpSender struct {
	SMTPConfig
}

func newSMTPSender(cfg *SMTPConfig) *smtpSender {
	if cfg == nil {
		return nil
	}
	return &smtpSender{SMTPConfig: *cfg}
}

// uncheckedPlainAuth implements smtp.Auth without enforcing that the connection
// is TLS. The stdlib smtp.PlainAuth refuses to send credentials over plaintext
// non-localhost connections; we delegate that security decision to the operator.
type uncheckedPlainAuth struct{ username, password string }

func (a uncheckedPlainAuth) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.username + "\x00" + a.password), nil
}

func (a uncheckedPlainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, fmt.Errorf("unexpected server challenge")
	}
	return nil, nil
}

const dialTimeout = 30 * time.Second

func (s *smtpSender) dial() (*smtp.Client, error) {
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)

	switch s.TLSMode {
	case "tls":
		conn, err := tls.DialWithDialer(
			&net.Dialer{Timeout: dialTimeout},
			"tcp", addr,
			&tls.Config{ServerName: s.Host},
		)
		if err != nil {
			return nil, fmt.Errorf("TLS dial: %w", err)
		}
		client, err := smtp.NewClient(conn, s.Host)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("smtp client: %w", err)
		}
		return client, nil

	default: // "none" or "starttls"
		conn, err := net.DialTimeout("tcp", addr, dialTimeout)
		if err != nil {
			return nil, fmt.Errorf("dial: %w", err)
		}
		client, err := smtp.NewClient(conn, s.Host)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("smtp client: %w", err)
		}
		if s.TLSMode == "starttls" {
			if err := client.StartTLS(&tls.Config{ServerName: s.Host}); err != nil {
				client.Close()
				return nil, fmt.Errorf("STARTTLS: %w", err)
			}
		}
		return client, nil
	}
}

func (s *smtpSender) sendOnce(subject, body string) error {
	client, err := s.dial()
	if err != nil {
		return err
	}
	defer client.Quit()

	if s.Username != "" {
		if err := client.Auth(uncheckedPlainAuth{s.Username, s.Password}); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}

	if err := client.Mail(s.FromEmail); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := client.Rcpt(s.ToEmail); err != nil {
		return fmt.Errorf("RCPT TO: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}

	from := s.FromEmail
	if s.FromName != "" {
		from = fmt.Sprintf(`"%s" <%s>`, s.FromName, s.FromEmail)
	}

	// Strip newlines from subject to prevent header injection.
	cleanSubject := strings.NewReplacer("\r", "", "\n", "").Replace(subject)

	var buf strings.Builder
	buf.WriteString("From: " + from + "\r\n")
	buf.WriteString("To: " + s.ToEmail + "\r\n")
	buf.WriteString("Subject: " + cleanSubject + "\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	buf.WriteString("\r\n")
	buf.WriteString(body)

	if _, err := fmt.Fprint(w, buf.String()); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return w.Close()
}

func (s *smtpSender) sendSuccess(startedAt, finishedAt time.Time, dbURLs []string) {
	var body strings.Builder
	body.WriteString("startedAt:  " + startedAt.UTC().Format(time.RFC3339) + "\r\n")
	body.WriteString("finishedAt: " + finishedAt.UTC().Format(time.RFC3339) + "\r\n")
	body.WriteString("databases:\r\n")
	for _, u := range dbURLs {
		body.WriteString("- " + u + "\r\n")
	}
	withRetry("smtp", func() error {
		return s.sendOnce("[pgsafe] Backup run succeeded", body.String())
	})
}

func (s *smtpSender) sendFailure(startedAt, finishedAt time.Time, failures []string) {
	var body strings.Builder
	body.WriteString("startedAt:  " + startedAt.UTC().Format(time.RFC3339) + "\r\n")
	body.WriteString("finishedAt: " + finishedAt.UTC().Format(time.RFC3339) + "\r\n")
	body.WriteString("failures:\r\n")
	for _, f := range failures {
		body.WriteString("- " + f + "\r\n")
	}
	withRetry("smtp", func() error {
		return s.sendOnce("[pgsafe] Backup run failed", body.String())
	})
}
