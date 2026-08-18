package models

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrProtected = errors.New("models: record is protected from deletion")

type Model struct {
	ID        uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AssignID mints a UUIDv7 when the caller did not supply one.
//
// v7 rather than v4 for the same reason the schema does: it is time-ordered, so
// consecutive inserts land next to each other in the primary key index instead of
// scattering across the whole B-tree. The memory fake and the tests call this;
// Postgres gets the same default from the ent mixin.
func (m *Model) AssignID() error {
	if m.ID != uuid.Nil {
		return nil
	}

	id, err := uuid.NewV7()
	if err != nil {
		return err
	}

	m.ID = id

	return nil
}

type SoftDelete struct {
	DeletedAt   *time.Time
	IsProtected bool
}

// RefuseIfProtected is the in-memory half of the protection the repository also
// enforces before a delete. The ent soft-delete hook cannot see is_protected
// without a query of its own, so the refusal lives here and at the call site.
func (s *SoftDelete) RefuseIfProtected() error {
	if s.IsProtected {
		return ErrProtected
	}

	return nil
}

// Delete marks the record deleted in memory only. Persisting it is the caller's
// job.
func (s *SoftDelete) Delete() error {
	if err := s.RefuseIfProtected(); err != nil {
		return err
	}

	now := time.Now().UTC()
	s.DeletedAt = &now

	return nil
}

func (s *SoftDelete) Restore() {
	s.DeletedAt = nil
}

func (s *SoftDelete) IsDeleted() bool {
	return s.DeletedAt != nil
}
