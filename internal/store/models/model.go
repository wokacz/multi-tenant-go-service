package models

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var ErrProtected = errors.New("models: record is protected from deletion")

type Model struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// BeforeCreate assigns a UUIDv7 rather than a v4. v7 is time-ordered, so
// consecutive inserts land next to each other in the primary key index instead
// of scattering across the whole B-tree.
func (m *Model) BeforeCreate(_ *gorm.DB) error {
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
	DeletedAt   gorm.DeletedAt `gorm:"index"`
	IsProtected bool
}

// BeforeDelete is where protection is actually enforced: GORM runs it for every
// delete, including db.Delete calls that never touch the Delete method below.
func (s *SoftDelete) BeforeDelete(_ *gorm.DB) error {
	if s.IsProtected {
		return ErrProtected
	}

	return nil
}

// Delete marks the record deleted in memory only. Persisting it is the caller's
// job; prefer db.Delete, which routes through BeforeDelete.
func (s *SoftDelete) Delete() error {
	if s.IsProtected {
		return ErrProtected
	}
	s.DeletedAt = gorm.DeletedAt{Time: time.Now().UTC(), Valid: true}

	return nil
}

func (s *SoftDelete) Restore() {
	s.DeletedAt = gorm.DeletedAt{}
}

func (s *SoftDelete) IsDeleted() bool {
	return s.DeletedAt.Valid
}
