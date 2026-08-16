package user

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/go-example/internal/store/models"
)

// TwoFactorCodeLength is the number of decimal digits emailed to finish a
// sign-in. It matches ResetCodeLength because it is the same kind of secret
// with the same defences around it, but it is its own constant: changing one
// flow's code length should not silently change the other's.
const TwoFactorCodeLength = 6

const (
	// twoFactorTTL is shorter than resetTTL. A reset code is read from a
	// mailbox the user may have to go and open; a sign-in code is expected
	// within the same minute, and the shorter window is free security.
	twoFactorTTL         = 10 * time.Minute
	twoFactorMaxAttempts = 5
)

// issueTwoFactorChallenge replaces any outstanding challenge with a fresh one
// bound to this device, and returns the plaintext for the caller to deliver.
func (s *Service) issueTwoFactorChallenge(ctx context.Context, userID, deviceID uuid.UUID, now time.Time) (string, error) {
	code, err := randomDigits(TwoFactorCodeLength)
	if err != nil {
		return "", fmt.Errorf("user: generate two-factor code: %w", err)
	}

	challenge := &models.TwoFactorChallenge{
		UserID:    userID,
		DeviceID:  deviceID,
		CodeHash:  s.hashCode(purposeTwoFactor, userID, code),
		ExpiresAt: now.Add(twoFactorTTL),
	}

	if err := s.repo.ReplaceTwoFactorChallenge(ctx, challenge); err != nil {
		return "", err
	}

	return code, nil
}

// VerifyTwoFactor spends a sign-in code and trusts the device it was raised
// for. It is reachable without credentials, so every way of getting it wrong —
// unknown address, unknown device, missing challenge, wrong device, wrong or
// expired or spent code — returns ErrInvalidTwoFactorCode.
func (s *Service) VerifyTwoFactor(
	ctx context.Context,
	email, code string,
	sc SignInContext,
) (*models.User, *models.Device, error) {
	u, err := s.repo.ByEmail(ctx, NormalizeEmail(email))
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return nil, nil, err
		}

		// Same decoy HMAC the reset flow runs, for the same reason: the work
		// done before the error should not depend on whether the address is
		// registered.
		s.hashCode(purposeTwoFactor, uuid.Nil, code)

		return nil, nil, ErrInvalidTwoFactorCode
	}

	if sc.DeviceToken == "" {
		s.hashCode(purposeTwoFactor, u.ID, code)

		return nil, nil, ErrInvalidTwoFactorCode
	}

	// The same rule as SignIn: verifying a code is the other way a token is
	// issued, so a suspension has to close it too.
	if u.IsSuspended() {
		return nil, nil, ErrSuspended
	}

	device, err := s.repo.DeviceByFingerprint(ctx, u.ID, deviceFingerprint(sc.DeviceToken))
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return nil, nil, err
		}

		s.hashCode(purposeTwoFactor, u.ID, code)

		return nil, nil, ErrInvalidTwoFactorCode
	}

	if device.IsRevoked() {
		return nil, nil, ErrDeviceRevoked
	}

	now := time.Now().UTC()

	challenge, err := s.repo.ActiveTwoFactorChallenge(ctx, u.ID, now)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return nil, nil, err
		}

		s.hashCode(purposeTwoFactor, u.ID, code)

		return nil, nil, ErrInvalidTwoFactorCode
	}

	// A code raised for one device must not be spendable from another,
	// otherwise the second factor proves only that someone can read the
	// mailbox — not that the machine asking to be let in is the one the code
	// was sent for.
	if challenge.DeviceID != device.ID ||
		!hmac.Equal([]byte(challenge.CodeHash), []byte(s.hashCode(purposeTwoFactor, u.ID, code))) {
		if failErr := s.repo.FailTwoFactorChallenge(ctx, challenge.ID, twoFactorMaxAttempts, now); failErr != nil {
			return nil, nil, failErr
		}

		s.tryRecordLogin(ctx, u.ID, &device.ID, sc, models.OutcomeMFAFailed)

		return nil, nil, ErrInvalidTwoFactorCode
	}

	if err := s.repo.ConsumeTwoFactorChallenge(ctx, challenge.ID, device.ID, now); err != nil {
		return nil, nil, err
	}

	if err := s.recordLogin(ctx, u.ID, &device.ID, sc, models.OutcomeSuccess); err != nil {
		return nil, nil, err
	}

	// Re-read so the caller sees the trust that was just granted rather than
	// the state it was loaded in.
	trusted, err := s.repo.ActiveDevice(ctx, u.ID, device.ID)
	if err != nil {
		// The only way the device can vanish between the transaction above and
		// this read is a concurrent revoke. Reporting that as ErrNotFound would
		// surface as "user not found", which is the wrong 404 about the wrong
		// thing.
		if errors.Is(err, ErrNotFound) {
			return nil, nil, ErrDeviceRevoked
		}

		return nil, nil, err
	}

	return u, trusted, nil
}

// SetTwoFactor turns the emailed second factor on or off. It re-checks the
// password: a stolen token should not be enough to disable the control that
// exists to contain a stolen token, nor to enable it and lock the owner out.
//
// Enabling also trusts the device the request came from. Without that, an
// account whose address no longer receives mail would be locked out by its own
// security setting, with no way back except a password reset to the same dead
// address.
func (s *Service) SetTwoFactor(ctx context.Context, userID uuid.UUID, password string, enabled bool, sc SignInContext) error {
	u, err := s.repo.ByID(ctx, userID)
	if err != nil {
		return err
	}

	if err := s.compareHash(ctx, []byte(u.PasswordHash), []byte(password)); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		return ErrInvalidCredentials
	}

	if enabled && sc.DeviceToken != "" {
		device, err := s.repo.DeviceByFingerprint(ctx, userID, deviceFingerprint(sc.DeviceToken))
		switch {
		case err == nil:
			if trustErr := s.repo.TrustDevice(ctx, device.ID); trustErr != nil {
				return trustErr
			}
		case errors.Is(err, ErrNotFound):
			// Nothing to trust. The caller keeps a working token until it
			// expires, and the next sign-in will ask for a code.
		default:
			return err
		}
	}

	return s.repo.SetTwoFactorEnabled(ctx, userID, enabled)
}
