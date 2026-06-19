// Package auth holds the pure authentication logic for the three planes:
//
//	ingest  — per-source API token (X-Api-Key), this file
//	read    — public, needs no auth
//	control — admin username/password -> JWT, see jwt.go
//
// Keeping the logic here (separate from HTTP wiring in package api) makes each
// plane unit-testable without spinning up a server.
package auth

import "crypto/subtle"

// TokenAuth resolves ingest API tokens to the single source each may write.
type TokenAuth struct {
	tokens map[string]string // token -> source
}

// NewTokenAuth builds a TokenAuth from a token->source map (from config).
func NewTokenAuth(tokens map[string]string) *TokenAuth {
	return &TokenAuth{tokens: tokens}
}

// SourceForToken returns the source a token is authorised to write, comparing in
// constant time to avoid leaking valid-prefix information via timing. An empty
// or unknown token yields ("", false).
func (a *TokenAuth) SourceForToken(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	// constant-time compare against every known token; never early-exit on a
	// length/prefix mismatch.
	var matchedSource string
	var matched bool
	for known, source := range a.tokens {
		if subtle.ConstantTimeCompare([]byte(token), []byte(known)) == 1 {
			matchedSource = source
			matched = true
		}
	}
	return matchedSource, matched
}
