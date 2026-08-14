package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
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

	// CreatedAt shadows Model.CreatedAt to supply the time column of the
	// composite indexes below. GORM only builds a composite index when several
	// fields share one index name, and the embedded field cannot be tagged
	// per-model.
	CreatedAt time.Time `gorm:"index:idx_login_user_time,priority:2;index:idx_login_device_time,priority:2"`

	UserID   uuid.UUID  `gorm:"type:uuid;not null;index:idx_login_user_time,priority:1"`
	DeviceID *uuid.UUID `gorm:"type:uuid;index:idx_login_device_time,priority:1"`

	IP        string       `gorm:"type:inet;not null;index"`
	UserAgent string       `gorm:"size:512"`
	Outcome   LoginOutcome `gorm:"size:20;not null;check:outcome IN ('success','bad_password','mfa_failed','locked')"`
	Country   string       `gorm:"size:2"`
}

// BeforeSave rejects unknown outcomes in Go, so callers get a useful error
// instead of a constraint violation from Postgres.
func (e *LoginEvent) BeforeSave(_ *gorm.DB) error {
	if !e.Outcome.Valid() {
		return fmt.Errorf("models: invalid login outcome %q", e.Outcome)
	}

	return nil
}
