// Package auth issues and verifies session tokens. It knows nothing about HTTP
// or storage: the API layer extracts the Bearer header, and the user service
// decides who is allowed to have a token.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidToken is the only error Parse returns. Distinguishing "malformed"
// from "expired" from "bad signature" would let a client probe the verifier.
var ErrInvalidToken = errors.New("auth: invalid token")

// jwtHeader is the HS256 JOSE header, pre-encoded so Issue and Parse agree on
// the exact bytes. Parse still decodes the header from the token and rejects
// anything else, which is what shuts out alg=none.
var jwtHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

type claims struct {
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
	Iat int64  `json:"iat"`
}

// Signer creates and verifies HMAC-SHA256 JWTs. The secret never leaves this
// type after construction.
type Signer struct {
	secret []byte
	ttl    time.Duration
}

// NewSigner copies secret so later mutations of the caller's string storage
// cannot change what is used to sign. Secret must be at least 32 bytes.
func NewSigner(secret string, ttl time.Duration) (*Signer, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("auth: token secret must be at least 32 bytes")
	}

	if ttl <= 0 {
		return nil, fmt.Errorf("auth: token TTL must be positive")
	}

	return &Signer{
		secret: []byte(secret),
		ttl:    ttl,
	}, nil
}

// Issue returns a compact JWT for userID and the instant it stops being valid.
func (s *Signer) Issue(userID uuid.UUID, now time.Time) (token string, expires time.Time, err error) {
	if userID == uuid.Nil {
		return "", time.Time{}, fmt.Errorf("auth: refuse to sign a token for a nil user id")
	}

	exp := now.Add(s.ttl)
	payload, err := json.Marshal(claims{
		Sub: userID.String(),
		Exp: exp.Unix(),
		Iat: now.Unix(),
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: encode claims: %w", err)
	}

	signing := jwtHeader + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(signing))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return signing + "." + sig, exp, nil
}

// Parse verifies the signature against the exact header.payload bytes in token
// (not a re-serialised form) and returns the subject. Every failure path is
// ErrInvalidToken.
func (s *Signer) Parse(token string, now time.Time) (uuid.UUID, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return uuid.Nil, ErrInvalidToken
	}

	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	want := mac.Sum(nil)

	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(want, got) {
		return uuid.Nil, ErrInvalidToken
	}

	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return uuid.Nil, ErrInvalidToken
	}

	var h struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(header, &h) != nil || h.Alg != "HS256" {
		return uuid.Nil, ErrInvalidToken
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return uuid.Nil, ErrInvalidToken
	}

	var c claims
	if json.Unmarshal(payload, &c) != nil {
		return uuid.Nil, ErrInvalidToken
	}

	if c.Exp <= now.Unix() {
		return uuid.Nil, ErrInvalidToken
	}

	id, err := uuid.Parse(c.Sub)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, ErrInvalidToken
	}

	return id, nil
}
