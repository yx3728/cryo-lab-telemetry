// Package config loads all runtime configuration from environment variables.
//
// Twelve-factor on purpose: there are no secrets or environment-specific values
// in the source tree. Everything the server needs — the database URL, ingest
// tokens, the admin password, the JWT signing secret, alert destinations — comes
// from the process environment, which is supplied by docker-compose / the host
// and is git-ignored (see .env.example for the shape).
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Config is the fully-parsed, validated configuration for the server.
type Config struct {
	DatabaseURL string
	HTTPAddr    string
	DBMaxConns  int

	// IngestTokens maps an opaque API token -> the single source it may write.
	// Binding a token to one source means a leaked lab token can only forge that
	// lab's data, never another instrument's.
	IngestTokens map[string]string

	// Admin (control plane) credentials and JWT settings.
	AdminUsername string
	AdminPassword string
	JWTSecret     []byte
	JWTTTL        time.Duration

	// Alerting. The daily email cap is a fallback default; the live value is the
	// admin-editable `alert_max_emails_per_day` config row.
	AlertMaxEmailsDay int
	SMTP              SMTPConfig
	AlertEmailTo      []string
	SlackWebhookURL   string
}

// SMTPConfig holds the optional email-notifier settings. If Host is empty, email
// alerting is disabled and threshold crosses are only logged.
type SMTPConfig struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
}

// Enabled reports whether enough SMTP settings are present to send mail.
func (s SMTPConfig) Enabled() bool { return s.Host != "" && s.From != "" }

// Load reads and validates configuration from the environment. It returns an
// error (rather than panicking) so main can log a clear message and exit.
func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		HTTPAddr:          envOr("HTTP_ADDR", ":8080"),
		DBMaxConns:        envInt("DB_MAX_CONNS", 10),
		IngestTokens:      parseIngestTokens(os.Getenv("INGEST_TOKENS")),
		AdminUsername:     envOr("ADMIN_USERNAME", "admin"),
		AdminPassword:     os.Getenv("ADMIN_PASSWORD"),
		JWTSecret:         []byte(os.Getenv("JWT_SECRET")),
		JWTTTL:            time.Duration(envInt("JWT_TTL_HOURS", 24)) * time.Hour,
		AlertMaxEmailsDay: envInt("ALERT_MAX_EMAILS_PER_DAY", 6),
		SMTP: SMTPConfig{
			Host:     os.Getenv("SMTP_HOST"),
			Port:     envOr("SMTP_PORT", "587"),
			Username: os.Getenv("SMTP_USERNAME"),
			Password: os.Getenv("SMTP_PASSWORD"),
			From:     os.Getenv("SMTP_FROM"),
		},
		AlertEmailTo:    splitNonEmpty(os.Getenv("ALERT_EMAIL_TO")),
		SlackWebhookURL: os.Getenv("SLACK_WEBHOOK_URL"),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if len(cfg.IngestTokens) == 0 {
		return nil, fmt.Errorf("INGEST_TOKENS is required (format: source:token[,source2:token2])")
	}
	if cfg.AdminPassword == "" {
		return nil, fmt.Errorf("ADMIN_PASSWORD is required")
	}
	if len(cfg.JWTSecret) < 16 {
		return nil, fmt.Errorf("JWT_SECRET is required and must be at least 16 bytes")
	}
	return cfg, nil
}

// parseIngestTokens turns "source:token,source2:token2" into token->source.
func parseIngestTokens(raw string) map[string]string {
	out := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			continue
		}
		source, token := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if source == "" || token == "" {
			continue
		}
		out[token] = source
	}
	return out
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

func splitNonEmpty(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
