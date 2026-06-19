// Command server is the lab-monitor cloud service: HTTPS ingest + read/control
// API backed by TimescaleDB. All configuration comes from the environment
// (see config.Load and .env.example). Run it behind Caddy, which terminates TLS.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yx3728/lab-monitor/server/internal/alert"
	"github.com/yx3728/lab-monitor/server/internal/api"
	"github.com/yx3728/lab-monitor/server/internal/auth"
	"github.com/yx3728/lab-monitor/server/internal/config"
	"github.com/yx3728/lab-monitor/server/internal/metricsx"
	"github.com/yx3728/lab-monitor/server/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Root context cancelled on SIGINT/SIGTERM for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Database: connect (with a short retry loop so we tolerate the DB container
	// still starting) and apply migrations.
	st, err := connectWithRetry(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		return err
	}
	log.Info("migrations applied")

	// Dependencies.
	tokenAuth := auth.NewTokenAuth(cfg.IngestTokens)
	jwtManager := auth.NewJWTManager(cfg.JWTSecret, cfg.JWTTTL, cfg.AdminUsername, cfg.AdminPassword)
	metrics := metricsx.New()
	alerter := alert.New(st, cfg.AlertDebounce, log, buildNotifiers(cfg, log)...)
	go alerter.Start(ctx) // periodic threshold reload

	srv := api.NewServer(st, tokenAuth, jwtManager, alerter, metrics, log)

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Serve until the context is cancelled, then shut down gracefully.
	serveErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.HTTPAddr)
		serveErr <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

// connectWithRetry tolerates the database not being ready yet (common when the
// whole stack starts together under docker-compose).
func connectWithRetry(ctx context.Context, url string, log *slog.Logger) (*store.Store, error) {
	const attempts = 30
	var lastErr error
	for i := 0; i < attempts; i++ {
		st, err := store.New(ctx, url)
		if err == nil {
			return st, nil
		}
		lastErr = err
		log.Info("waiting for database", "attempt", i+1, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil, lastErr
}

// buildNotifiers wires up whichever alert channels are configured. If none are,
// the alerter runs in log-only mode (threshold crosses still hit alert_log).
func buildNotifiers(cfg *config.Config, log *slog.Logger) []alert.Notifier {
	var notifiers []alert.Notifier
	if cfg.SMTP.Enabled() && len(cfg.AlertEmailTo) > 0 {
		notifiers = append(notifiers, alert.NewEmailNotifier(
			cfg.SMTP.Host, cfg.SMTP.Port, cfg.SMTP.Username, cfg.SMTP.Password,
			cfg.SMTP.From, cfg.AlertEmailTo))
		log.Info("email alerting enabled", "to", cfg.AlertEmailTo)
	}
	if cfg.SlackWebhookURL != "" {
		notifiers = append(notifiers, alert.NewSlackNotifier(cfg.SlackWebhookURL, &http.Client{Timeout: 10 * time.Second}))
		log.Info("slack alerting enabled")
	}
	if len(notifiers) == 0 {
		log.Info("no alert notifiers configured; running in log-only mode")
	}
	return notifiers
}
