package ent

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/membership"
)

func (m *Membership) Activate(at time.Time) {
	m.Status = membership.StatusActive

	if m.JoinedAt == nil {
		joined := at.UTC()
		m.JoinedAt = &joined
	}
}

func (m *Membership) Suspend() {
	m.Status = membership.StatusSuspended
}

func (m *Membership) IsActive() bool {
	return m.Status.GrantsPermissions()
}

func (m *Membership) Validate() error {
	if err := membership.StatusValidator(m.Status); err != nil {
		return err
	}

	if m.UserID == uuid.Nil {
		return fmt.Errorf("ent: membership has no account")
	}

	return nil
}
