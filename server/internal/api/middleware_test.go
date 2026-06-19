package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yx3728/lab-monitor/server/internal/auth"
)

// testServer builds a Server with only the auth dependencies the middleware
// needs; store/alerter/metrics are unused by these middlewares.
func testServer() *Server {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tokenAuth := auth.NewTokenAuth(map[string]string{"good-token": "unisoku-stm"})
	jwt := auth.NewJWTManager([]byte("test-secret-at-least-16-bytes!!"), time.Hour, "admin", "pw")
	return NewServer(nil, tokenAuth, jwt, nil, nil, log)
}

func TestRequireIngestToken(t *testing.T) {
	s := testServer()
	// Handler that echoes the authenticated source from context.
	h := s.requireIngestToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		src, _ := sourceFromContext(r.Context())
		_, _ = io.WriteString(w, src)
	}))

	t.Run("missing key -> 401", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/ingest", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("bad key -> 401", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/ingest", nil)
		req.Header.Set("X-Api-Key", "nope")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("good key -> 200 and source in context", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/ingest", nil)
		req.Header.Set("X-Api-Key", "good-token")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.String() != "unisoku-stm" {
			t.Fatalf("source = %q, want unisoku-stm", rec.Body.String())
		}
	})
}

func TestRequireAdmin(t *testing.T) {
	s := testServer()
	h := s.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("no header -> 401", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/config", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("garbage bearer -> 401", func(t *testing.T) {
		req := httptest.NewRequest("PUT", "/api/config", nil)
		req.Header.Set("Authorization", "Bearer not.a.jwt")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("valid jwt -> 200", func(t *testing.T) {
		token, _, _ := s.jwt.Issue("admin")
		req := httptest.NewRequest("PUT", "/api/config", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})
}
