package models

import (
	"fmt"

	"github.com/google/uuid"
)

// LoginOutcome enumerates the recognised results of a login attempt.
type LoginOutcome string

const (
	OutcomeSuccess     LoginOutcome = "success"
	OutcomeBadPassword LoginOutcome = "bad_password"
	OutcomeMFAFailed   LoginOutcome = "mfa_failed"
	OutcomeLocked      LoginOutcome = "locked"
)

func (o LoginOutcome) Valid() bool {
	switch o {
	case OutcomeSuccess, OutcomeBadPassword, OutcomeMFAFailed, OutcomeLocked:
		return true
	default:
		return false
	}
}

type LoginEvent struct {
	Model

	UserID   uuid.UUID
	DeviceID *uuid.UUID

	IP        string
	UserAgent string
	Outcome   LoginOutcome
	Country   string
}

// Validate rejects unknown outcomes in Go, so callers get a useful error
// instead of a constraint violation from Postgres.
func (e *LoginEvent) Validate() error {
	if !e.Outcome.Valid() {
		return fmt.Errorf("models: invalid login outcome %q", e.Outcome)
	}

	return nil
}
