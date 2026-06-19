// Package api wires the HTTP surface: routing, the three auth planes as
// middleware, and the handlers. All business state lives in the store; handlers
// are stateless so any number of them can run concurrently (goroutine per
// request, the standard net/http model).
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/yx3728/lab-monitor/server/internal/alert"
	"github.com/yx3728/lab-monitor/server/internal/auth"
	"github.com/yx3728/lab-monitor/server/internal/metricsx"
	"github.com/yx3728/lab-monitor/server/internal/store"
)

// Server bundles the handler dependencies.
type Server struct {
	store     *store.Store
	tokenAuth *auth.TokenAuth
	jwt       *auth.JWTManager
	alerter   *alert.Alerter
	metrics   *metricsx.Tracker
	log       *slog.Logger
	startedAt time.Time
}

// NewServer constructs a Server from its dependencies.
func NewServer(
	st *store.Store,
	tokenAuth *auth.TokenAuth,
	jwt *auth.JWTManager,
	alerter *alert.Alerter,
	metrics *metricsx.Tracker,
	log *slog.Logger,
) *Server {
	return &Server{
		store:     st,
		tokenAuth: tokenAuth,
		jwt:       jwt,
		alerter:   alerter,
		metrics:   metrics,
		log:       log,
		startedAt: time.Now(),
	}
}

// Router builds the chi router with all routes and middleware.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.requestLogger)
	r.Use(middleware.Recoverer)
	r.Use(corsPublic) // dashboard reads are public and may be cross-origin in dev

	// Ops endpoints (public).
	r.Get("/healthz", s.handleHealth)
	r.Get("/metrics", s.handleMetrics)

	// Ingest plane: per-source API token.
	r.With(s.requireIngestToken).Post("/ingest", s.handleIngest)

	r.Route("/api", func(r chi.Router) {
		// Control plane: login issues a JWT.
		r.Post("/login", s.handleLogin)

		// Read plane: public, no auth.
		r.Get("/series", s.handleSeries)
		r.Get("/export.csv", s.handleExportCSV)
		r.Get("/config", s.handleGetConfig)

		// Control plane: JWT-gated writes.
		r.With(s.requireAdmin).Put("/config", s.handlePutConfig)
	})

	return r
}

// --- small response helpers --------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError returns a JSON error envelope. Messages are deliberately generic on
// the auth paths so we don't reveal which half of a credential was wrong.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// requestLogger logs method, path, status, and duration via slog.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		s.log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"dur_ms", time.Since(start).Milliseconds(),
		)
	})
}

// corsPublic allows cross-origin reads (the dashboard in local dev hits the API
// on a different port). We use Bearer tokens, not cookies, so a wildcard origin
// is safe — no Allow-Credentials is set.
func corsPublic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Api-Key")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
