package user

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

// deviceTokenBytes is the length of the opaque secret a client keeps to be
// recognised on its next sign-in. It is the only thing standing between an
// attacker who has the password and a device that skips the second factor, so
// it is sized like a session secret rather than like a code a human types.
const deviceTokenBytes = 32

// maxUserAgentLength matches the column. A header longer than this is
// truncated rather than rejected: a weird client should not be able to make a
// sign-in fail, and the value is only ever shown back to the account holder.
const maxUserAgentLength = 512

// MaxLoginEvents bounds the history page. The composite index on
// (user_id, created_at) makes the query cheap; the cap is about response size.
// It is exported so the API schema can advertise the same number the service
// enforces — see the compile-time assertion in internal/api/v1/devices.go.
const MaxLoginEvents = 50

// unknownIP is what goes in the login event when the peer address cannot be
// parsed. The column is inet and NOT NULL, and losing the whole audit row
// because a listener reported something odd would be the worse trade.
const unknownIP = "0.0.0.0"

// SignInContext describes where an attempt came from.
//
// IP and UserAgent are recorded on the login event and the device, so they are
// domain data rather than transport trivia. DeviceToken is the opaque secret
// the client kept from a previous sign-in; it is empty on a first visit.
type SignInContext struct {
	IP          string
	UserAgent   string
	DeviceToken string
}

// SignInResult is what the password, the device and the second-factor rules
// jointly decided.
type SignInResult struct {
	User   *ent.User
	Device *ent.Device

	// DeviceToken is set only when this sign-in minted a new device. It is
	// returned once and never recoverable, so a client that drops it simply
	// gets a new device row next time.
	DeviceToken string

	// Challenged means no session was granted: Code has been generated and
	// the caller must deliver it and wait for VerifyTwoFactor.
	Challenged bool

	// Code is the plaintext second factor. This package does not send mail;
	// the caller delivers it and must never put it in a response body.
	Code string
}

// SignIn checks the password, records the attempt, recognises or registers the
// device, and decides whether a second factor is still owed.
//
// It returns ErrInvalidCredentials for both a wrong password and an unknown
// address, on the same bcrypt-paced path as Authenticate.
func (s *Service) SignIn(ctx context.Context, email, password string, sc SignInContext) (*SignInResult, error) {
	u, err := s.repo.ByEmail(ctx, NormalizeEmail(email))
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}

		// The same decoy hash Authenticate runs. No login event is written:
		// LoginEvent.UserID is NOT NULL, and there is no user to attribute the
		// attempt to. Recording misses would need a table keyed by the
		// submitted address, which is a store of unauthenticated input that
		// nothing in this service would ever clean up.
		_ = s.compareHash(ctx, s.dummyHash, []byte(password))

		return nil, ErrInvalidCredentials
	}

	if err := s.compareHash(ctx, []byte(u.PasswordHash), []byte(password)); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// A failure to write the audit row is swallowed rather than returned.
		// Returning it would answer a wrong password with a 500 while an
		// unknown address still got a 401 — exactly the account oracle the
		// shared error is there to prevent.
		s.tryRecordLogin(ctx, u.ID, nil, sc, ent.OutcomeBadPassword)

		return nil, ErrInvalidCredentials
	}

	// Checked after the password and before a device is minted or a token is
	// issued. After, because telling an anonymous caller that an address is
	// suspended would answer a question they have not earned; before, because
	// this is the only place a *new* token comes from — the bearer middleware
	// blocks the ones already out, and without this a suspended account could
	// simply sign in again and get a fresh one.
	if u.IsSuspended() {
		s.tryRecordLogin(ctx, u.ID, nil, sc, ent.OutcomeLocked)

		return nil, ErrSuspended
	}

	device, token, err := s.resolveDevice(ctx, u.ID, sc)
	if err != nil {
		return nil, err
	}

	if device.IsRevoked() {
		s.tryRecordLogin(ctx, u.ID, &device.ID, sc, ent.OutcomeLocked)

		return nil, ErrDeviceRevoked
	}

	now := time.Now().UTC()
	if err := s.repo.TouchDevice(ctx, device.ID, now, normalizeIP(sc.IP), truncate(sc.UserAgent, maxUserAgentLength)); err != nil {
		return nil, err
	}

	if u.TwoFactorEnabled && !device.IsTrusted() {
		code, err := s.issueTwoFactorChallenge(ctx, u.ID, device.ID, now)
		if err != nil {
			return nil, err
		}

		// No login event yet. The enum records outcomes, and "we asked for a
		// code" is not one — success or mfa_failed is written by VerifyTwoFactor
		// once the attempt actually resolves.
		return &SignInResult{User: u, Device: device, DeviceToken: token, Challenged: true, Code: code}, nil
	}

	// Here the error is returned. A token handed out without its audit row is
	// a gap in the history the account holder is meant to be able to trust.
	if err := s.recordLogin(ctx, u.ID, &device.ID, sc, ent.OutcomeSuccess); err != nil {
		return nil, err
	}

	return &SignInResult{User: u, Device: device, DeviceToken: token}, nil
}

// resolveDevice recognises the client's device or registers a new one. The
// second result is a freshly minted device token, empty when the device was
// already known.
func (s *Service) resolveDevice(ctx context.Context, userID uuid.UUID, sc SignInContext) (*ent.Device, string, error) {
	if sc.DeviceToken != "" {
		device, err := s.repo.DeviceByFingerprint(ctx, userID, deviceFingerprint(sc.DeviceToken))
		if err == nil {
			return device, "", nil
		}

		if !errors.Is(err, ErrNotFound) {
			return nil, "", err
		}
		// An unrecognised token is not an error. It is what a token from
		// another account, or from a database that has since been reset,
		// looks like — the client simply gets a new one below.
	}

	token, err := randomToken()
	if err != nil {
		return nil, "", err
	}

	device := &ent.Device{
		UserID:      userID,
		Fingerprint: deviceFingerprint(token),
		UserAgent:   truncate(sc.UserAgent, maxUserAgentLength),
	}

	if err := s.repo.CreateDevice(ctx, device); err != nil {
		return nil, "", err
	}

	return device, token, nil
}

// Devices lists the caller's known devices, most recently seen first.
func (s *Service) Devices(ctx context.Context, userID uuid.UUID) ([]ent.Device, error) {
	return s.repo.Devices(ctx, userID)
}

// RevokeDevice blocks a device and drops its trust. Revoking the device the
// caller is currently on is allowed and is how "sign out here" is spelled:
// the bearer middleware checks the device on every request, so the token stops
// working immediately rather than at expiry.
func (s *Service) RevokeDevice(ctx context.Context, userID, deviceID uuid.UUID) error {
	return s.repo.RevokeDevice(ctx, userID, deviceID)
}

// LoginEvents returns the caller's recent sign-in history, newest first.
func (s *Service) LoginEvents(ctx context.Context, userID uuid.UUID, limit int) ([]ent.LoginEvent, error) {
	if limit <= 0 || limit > MaxLoginEvents {
		limit = MaxLoginEvents
	}

	return s.repo.LoginEvents(ctx, userID, limit)
}

// ActiveDevice is the bearer middleware's check that the device a token names
// still exists and has not been revoked.
func (s *Service) ActiveDevice(ctx context.Context, userID, deviceID uuid.UUID) (*ent.Device, error) {
	return s.repo.ActiveDevice(ctx, userID, deviceID)
}

func (s *Service) recordLogin(
	ctx context.Context,
	userID uuid.UUID,
	deviceID *uuid.UUID,
	sc SignInContext,
	outcome ent.LoginOutcome,
) error {
	return s.repo.RecordLoginEvent(ctx, &ent.LoginEvent{
		UserID:    userID,
		DeviceID:  deviceID,
		IP:        normalizeIP(sc.IP),
		UserAgent: truncate(sc.UserAgent, maxUserAgentLength),
		Outcome:   outcome,
	})
}

// tryRecordLogin is recordLogin on the paths where a storage failure must not
// change the answer the caller gets. Each call site says why.
func (s *Service) tryRecordLogin(
	ctx context.Context,
	userID uuid.UUID,
	deviceID *uuid.UUID,
	sc SignInContext,
	outcome ent.LoginOutcome,
) {
	_ = s.recordLogin(ctx, userID, deviceID, sc, outcome)
}

// deviceFingerprint is what the store holds instead of the device token. The
// hex digest is 64 characters, which is the width of Device.Fingerprint.
func deviceFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}

func randomToken() (string, error) {
	buf := make([]byte, deviceTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("user: generate device token: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}

	return s[:max]
}

// normalizeIP keeps unparseable input out of an inet column. net/http always
// reports host:port for a TCP listener, so the fallback is defensive.
func normalizeIP(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return unknownIP
	}

	return parsed.String()
}
