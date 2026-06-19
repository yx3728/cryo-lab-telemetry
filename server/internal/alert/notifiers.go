package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"strings"
)

// EmailNotifier sends alerts over SMTP. It is constructed only when SMTP is
// configured; otherwise the alerter runs in log-only mode.
type EmailNotifier struct {
	host string
	port string
	auth smtp.Auth // nil if no credentials configured
	from string
	to   []string
}

// NewEmailNotifier builds an SMTP notifier. If username is empty, the connection
// is made without auth (useful for local relays).
func NewEmailNotifier(host, port, username, password, from string, to []string) *EmailNotifier {
	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}
	return &EmailNotifier{host: host, port: port, auth: auth, from: from, to: to}
}

// Send delivers one alert email to all recipients.
func (e *EmailNotifier) Send(_ context.Context, subject, body string) error {
	msg := bytes.NewBuffer(nil)
	fmt.Fprintf(msg, "From: %s\r\n", e.from)
	fmt.Fprintf(msg, "To: %s\r\n", strings.Join(e.to, ", "))
	fmt.Fprintf(msg, "Subject: %s\r\n", subject)
	fmt.Fprint(msg, "MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n")
	msg.WriteString(body)
	addr := e.host + ":" + e.port
	return smtp.SendMail(addr, e.auth, e.from, e.to, msg.Bytes())
}

// SlackNotifier posts alerts to a Slack incoming webhook.
type SlackNotifier struct {
	webhookURL string
	client     *http.Client
}

// NewSlackNotifier builds a Slack notifier for the given webhook URL.
func NewSlackNotifier(webhookURL string, client *http.Client) *SlackNotifier {
	if client == nil {
		client = http.DefaultClient
	}
	return &SlackNotifier{webhookURL: webhookURL, client: client}
}

// Send posts the alert as a Slack message.
func (s *SlackNotifier) Send(ctx context.Context, subject, body string) error {
	payload, err := json.Marshal(map[string]string{"text": subject + "\n" + body})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned status %d", resp.StatusCode)
	}
	return nil
}
