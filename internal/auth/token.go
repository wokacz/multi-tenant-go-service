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
	// Iss and Aud are what stops a token minted by one installation from working
	// in another that happens to share the secret — staging and production
	// configured from the same secrets file is not a hypothetical.
	//
	// They carry the same string today, and that is not an oversight: there is one
	// service here, so whoever signed the token is also the only party meant to
	// accept it. Both are written and both are checked, so the day a second
	// service appears the audience is what splits, and tokens issued for this API
	// will not be accepted by it.
	Iss string `json:"iss"`
	Aud string `json:"aud"`
	// Ver is the user's session epoch at issue time. Password changes bump
	// the epoch so a still-unexpired token issued before the change is
	// rejected without a denylist.
	Ver int `json:"ver"`
	// Did is the device the token was issued to. Revoking that device has to
	// take effect on tokens already handed out, and it cannot without the
	// token naming which device it belongs to.
	Did string `json:"did"`
}

// Session is what a token stands for: a subject, the device it was issued to
// and the epoch it was issued under. It is a struct rather than three return
// values because every caller needs all three, and a bare (uuid, uuid, int)
// signature is trivially transposable at the call site.
type Session struct {
	UserID   uuid.UUID
	DeviceID uuid.UUID
	Epoch    int
}

// Signer creates and verifies HMAC-SHA256 JWTs. The secret never leaves this
// type after construction.
type Signer struct {
	secret []byte
	ttl    time.Duration
	issuer string
}

// NewSigner copies secret so later mutations of the caller's string storage
// cannot change what is used to sign. Secret must be at least 32 bytes.
func NewSigner(secret string, ttl time.Duration, issuer string) (*Signer, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("auth: token secret must be at least 32 bytes")
	}

	if ttl <= 0 {
		return nil, fmt.Errorf("auth: token TTL must be positive")
	}

	if issuer == "" {
		return nil, fmt.Errorf("auth: token issuer must not be empty")
	}

	return &Signer{
		secret: []byte(secret),
		ttl:    ttl,
		issuer: issuer,
	}, nil
}

// Issue returns a compact JWT for sess, and the instant it stops being valid.
func (s *Signer) Issue(sess Session, now time.Time) (token string, expires time.Time, err error) {
	if sess.UserID == uuid.Nil {
		return "", time.Time{}, fmt.Errorf("auth: refuse to sign a token for a nil user id")
	}

	if sess.DeviceID == uuid.Nil {
		return "", time.Time{}, fmt.Errorf("auth: refuse to sign a token for a nil device id")
	}

	exp := now.Add(s.ttl)
	payload, err := json.Marshal(claims{
		Sub: sess.UserID.String(),
		Exp: exp.Unix(),
		Iat: now.Unix(),
		Ver: sess.Epoch,
		Did: sess.DeviceID.String(),
		Iss: s.issuer,
		Aud: s.issuer,
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
// (not a re-serialised form) and returns the session it stands for. Every
// failure path is ErrInvalidToken.
func (s *Signer) Parse(token string, now time.Time) (Session, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Session{}, ErrInvalidToken
	}

	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	want := mac.Sum(nil)

	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(want, got) {
		return Session{}, ErrInvalidToken
	}

	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Session{}, ErrInvalidToken
	}

	var h struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(header, &h) != nil || h.Alg != "HS256" {
		return Session{}, ErrInvalidToken
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Session{}, ErrInvalidToken
	}

	var c claims
	if json.Unmarshal(payload, &c) != nil {
		return Session{}, ErrInvalidToken
	}

	if c.Exp <= now.Unix() {
		return Session{}, ErrInvalidToken
	}

	// A token from another installation is refused here rather than at the
	// signature: the signature can agree while the token was never meant for this
	// service. Both claims are compared, not just one, because a token missing
	// either is a token from before they existed — and accepting those would make
	// the check optional, which is the same as not having it.
	if c.Iss != s.issuer || c.Aud != s.issuer {
		return Session{}, ErrInvalidToken
	}

	id, err := uuid.Parse(c.Sub)
	if err != nil || id == uuid.Nil {
		return Session{}, ErrInvalidToken
	}

	deviceID, err := uuid.Parse(c.Did)
	if err != nil || deviceID == uuid.Nil {
		return Session{}, ErrInvalidToken
	}

	return Session{UserID: id, DeviceID: deviceID, Epoch: c.Ver}, nil
}
