package user

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

// EmailChangeCodeLength matches the reset code: short enough to type, and with
// the attempt cap and the rate limiter, expensive enough to guess.
const EmailChangeCodeLength = 6

const (
	emailChangeTTL         = 15 * time.Minute
	emailChangeMaxAttempts = 5
)

// BeginEmailChange sends a confirmation code to a new address and returns it for
// delivery.
//
// The account's address is not touched. Until the code comes back, the only thing
// that has happened is that somebody asked — which matters because the address on
// an account is what receives password resets, so changing it on request alone
// would turn this into a way to take over an account with a borrowed token.
//
// The current password is required for the same reason SetTwoFactorEnabled
// requires it: a token that leaked out of a browser must not be enough to
// redirect where the account can be recovered from.
//
// It reveals nothing about whether the new address is already registered. An
// authenticated caller could otherwise walk a list of addresses one request at a
// time, which is the oracle registration goes to some length to close. The answer
// exists, but it is given at confirmation — by which point the caller has read a
// code out of that mailbox, and somebody who can do that was going to find out
// anyway.
func (s *Service) BeginEmailChange(ctx context.Context, userID uuid.UUID, newEmail, password string) (string, error) {
	newEmail = NormalizeEmail(newEmail)
	if newEmail == "" {
		return "", ErrEmailInvalid
	}

	u, err := s.repo.ByID(ctx, userID)
	if err != nil {
		return "", err
	}

	if err := s.compareHash(ctx, []byte(u.PasswordHash), []byte(password)); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		return "", ErrInvalidCredentials
	}

	if newEmail == u.Email {
		return "", ErrSameEmail
	}

	code, err := randomDigits(EmailChangeCodeLength)
	if err != nil {
		return "", fmt.Errorf("user: generate email change code: %w", err)
	}

	now := time.Now().UTC()
	change := &ent.EmailChange{
		UserID:    u.ID,
		NewEmail:  newEmail,
		CodeHash:  s.hashCode(purposeEmailChange, u.ID, code),
		ExpiresAt: now.Add(emailChangeTTL),
	}

	if err := s.repo.ReplaceEmailChange(ctx, change); err != nil {
		return "", err
	}

	return code, nil
}

// PendingEmailChange is the address a confirmation code was sent to, for the
// handler to address the mail to. It is separate from BeginEmailChange only
// because that already returns the code.
func (s *Service) PendingEmailChange(ctx context.Context, userID uuid.UUID) (string, error) {
	change, err := s.repo.ActiveEmailChange(ctx, userID, time.Now().UTC())
	if err != nil {
		return "", err
	}

	return change.NewEmail, nil
}

// ConfirmEmailChange applies the address when the code is right.
//
// The session epoch is deliberately not bumped. The password has not changed, so
// existing sessions are no less legitimate than they were a moment ago, and
// signing everybody out on a profile change is a surprise with no security
// payoff — what protects this operation is the password at the start and the code
// from the new mailbox at the end.
func (s *Service) ConfirmEmailChange(ctx context.Context, userID uuid.UUID, code string) error {
	now := time.Now().UTC()

	change, err := s.repo.ActiveEmailChange(ctx, userID, now)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Hashed anyway, so a request with no outstanding change costs the
			// same as one with a wrong code.
			s.hashCode(purposeEmailChange, userID, code)

			return ErrInvalidEmailCode
		}

		return err
	}

	if s.hashCode(purposeEmailChange, userID, code) != change.CodeHash {
		if err := s.repo.FailEmailChange(ctx, change.ID, emailChangeMaxAttempts, now); err != nil {
			return err
		}

		return ErrInvalidEmailCode
	}

	consumed := now
	change.ConsumedAt = &consumed

	return s.repo.ConsumeEmailChange(ctx, change, change.NewEmail)
}
