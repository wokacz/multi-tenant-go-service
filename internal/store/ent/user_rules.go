package ent

import (
	"time"

	"github.com/google/uuid"
)

func (u *User) IsSuspended() bool {
	return u.SuspendedAt != nil
}

func (u *User) IsDeleted() bool {
	return u.DeletedAt != nil
}

func (u *User) RefuseIfProtected() error {
	if u.IsProtected {
		return ErrProtected
	}

	return nil
}

func (u *User) RefuseDelete() error {
	if err := u.RefuseIfProtected(); err != nil {
		return err
	}

	if u.ID == uuid.Nil {
		return ErrBatchDeleteUnsupported
	}

	return nil
}

func (u *User) Delete() error {
	if err := u.RefuseIfProtected(); err != nil {
		return err
	}

	now := time.Now().UTC()
	u.DeletedAt = &now

	return nil
}

func (u *User) Restore() {
	u.DeletedAt = nil
}
