package auth

import (
	"crypto/subtle"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTManager issues and verifies the admin (control-plane) tokens. Tokens are
// signed with HS256 using a secret from the environment; nothing here is stored
// in the database.
type JWTManager struct {
	secret   []byte
	ttl      time.Duration
	username string
	password string
	now      func() time.Time // injectable clock for tests
}

// NewJWTManager configures the manager with the signing secret, token TTL, and
// the single admin credential pair (from env).
func NewJWTManager(secret []byte, ttl time.Duration, username, password string) *JWTManager {
	return &JWTManager{secret: secret, ttl: ttl, username: username, password: password, now: time.Now}
}

// CheckCredentials verifies an admin login in constant time.
func (m *JWTManager) CheckCredentials(username, password string) bool {
	// Compare both fields with constant-time compare so neither a wrong username
	// nor a wrong password is distinguishable by timing.
	uOK := subtle.ConstantTimeCompare([]byte(username), []byte(m.username)) == 1
	pOK := subtle.ConstantTimeCompare([]byte(password), []byte(m.password)) == 1
	return uOK && pOK
}

// Issue returns a signed JWT for the admin subject.
func (m *JWTManager) Issue(username string) (string, time.Time, error) {
	now := m.now()
	expires := now.Add(m.ttl)
	claims := jwt.RegisteredClaims{
		Subject:   username,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(expires),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expires, nil
}

// Verify parses and validates a token string, returning the subject on success.
func (m *JWTManager) Verify(tokenString string) (string, error) {
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		// Reject any algorithm other than the one we issue with — this prevents
		// the classic "alg: none" and RS/HS confusion attacks.
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		return "", err
	}
	return claims.Subject, nil
}
