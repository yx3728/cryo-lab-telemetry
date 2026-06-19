package auth

import (
	"testing"
	"time"
)

func TestSourceForToken(t *testing.T) {
	a := NewTokenAuth(map[string]string{
		"tok-stm":  "unisoku-stm",
		"tok-fast": "stm-fast",
	})

	tests := []struct {
		name       string
		token      string
		wantSource string
		wantOK     bool
	}{
		{"valid stm token", "tok-stm", "unisoku-stm", true},
		{"valid fast token", "tok-fast", "stm-fast", true},
		{"unknown token", "nope", "", false},
		{"empty token", "", "", false},
		{"near-miss token", "tok-st", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src, ok := a.SourceForToken(tc.token)
			if ok != tc.wantOK || src != tc.wantSource {
				t.Fatalf("SourceForToken(%q) = (%q,%v), want (%q,%v)",
					tc.token, src, ok, tc.wantSource, tc.wantOK)
			}
		})
	}
}

func newTestJWT() *JWTManager {
	return NewJWTManager([]byte("test-secret-at-least-16-bytes!!"), time.Hour, "admin", "s3cret")
}

func TestCheckCredentials(t *testing.T) {
	m := newTestJWT()
	if !m.CheckCredentials("admin", "s3cret") {
		t.Fatal("correct credentials rejected")
	}
	if m.CheckCredentials("admin", "wrong") {
		t.Fatal("wrong password accepted")
	}
	if m.CheckCredentials("root", "s3cret") {
		t.Fatal("wrong username accepted")
	}
}

func TestJWTRoundTrip(t *testing.T) {
	m := newTestJWT()
	token, expires, err := m.Issue("admin")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !expires.After(time.Now()) {
		t.Fatal("expiry should be in the future")
	}
	sub, err := m.Verify(token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if sub != "admin" {
		t.Fatalf("subject = %q, want admin", sub)
	}
}

func TestJWTRejectsExpired(t *testing.T) {
	m := newTestJWT()
	// Issue a token whose lifetime is already in the past by moving the manager's
	// clock back two hours (TTL is one hour, so exp lands ~one hour ago).
	m.now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	token, _, err := m.Issue("admin")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := m.Verify(token); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestJWTRejectsWrongSecret(t *testing.T) {
	signer := newTestJWT()
	token, _, _ := signer.Issue("admin")

	attacker := NewJWTManager([]byte("a-totally-different-secret-key!!"), time.Hour, "admin", "s3cret")
	if _, err := attacker.Verify(token); err == nil {
		t.Fatal("expected token signed with a different secret to be rejected")
	}
}

func TestJWTRejectsTampered(t *testing.T) {
	m := newTestJWT()
	token, _, _ := m.Issue("admin")
	// Flip the last character of the signature.
	tampered := token[:len(token)-1]
	if token[len(token)-1] == 'a' {
		tampered += "b"
	} else {
		tampered += "a"
	}
	if _, err := m.Verify(tampered); err == nil {
		t.Fatal("expected tampered token to be rejected")
	}
}
