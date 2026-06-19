package api

import (
	"context"
	"net/http"
	"strings"
)

// ctxKey is an unexported type for context keys to avoid collisions.
type ctxKey int

const (
	ctxKeySource  ctxKey = iota // authenticated ingest source
	ctxKeySubject               // authenticated admin subject
)

// requireIngestToken authenticates the ingest plane. It reads X-Api-Key,
// resolves it to the single source that token may write, and stashes the source
// in the request context for the handler.
func (s *Server) requireIngestToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Api-Key")
		source, ok := s.tokenAuth.SourceForToken(token)
		if !ok {
			writeError(w, http.StatusUnauthorized, "invalid or missing X-Api-Key")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeySource, source)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAdmin authenticates the control plane. It expects an
// "Authorization: Bearer <jwt>" header and validates the token's signature and
// expiry.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		subject, err := s.jwt.Verify(strings.TrimPrefix(header, prefix))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeySubject, subject)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// sourceFromContext returns the authenticated ingest source set by
// requireIngestToken.
func sourceFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKeySource).(string)
	return v, ok
}
