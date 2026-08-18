package models

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrBatchDeleteUnsupported = errors.New("models: deleting a user requires a primary key so its devices can be revoked")

type User struct {
	Model
	SoftDelete

	Name string
	// Unique only among live accounts. A plain unique index plus a soft delete
	// means a deleted account occupies its address for ever: nobody could register
	// it again, and because registration hides a duplicate behind 204 to avoid an
	// enumeration oracle, the person trying would be told it worked and then never
	// be able to sign in. The partial index frees the address while the old row —
	// and the address on it — stays, so the audit trail can still say who did what.
	Email        string
	PasswordHash string `json:"-"`

	// SessionEpoch is copied into the JWT at issue time and incremented when
	// the password changes. Tokens from before the change then fail even
	// though their signature and expiry are still valid.
	SessionEpoch int

	// TwoFactorEnabled turns on the emailed second factor. It gates sign-in
	// from devices the account has not trusted yet; a trusted device skips
	// the challenge, which is what keeps the flow usable day to day.
	TwoFactorEnabled bool

	// SuspendedAt blocks the account everywhere without deleting it.
	//
	// It is separate from the soft delete because the two mean different things
	// to an administrator and to the person: a suspension is reversible and the
	// account keeps its memberships and history, a deletion is not. The bearer
	// middleware checks it on every request, which is what makes suspending take
	// effect on tokens that were already handed out — the same promise device
	// revocation makes.
	SuspendedAt *time.Time

	// Locale is the account's preferred language as a BCP 47 tag, empty when
	// the user has not chosen one. It outranks Accept-Language for responses,
	// and it is the only language signal mail has: a message sent from a
	// background job has no request headers to negotiate from.
	Locale string

	Devices     []Device     `json:"-"`
	LoginEvents []LoginEvent `json:"-"`
}

// IsSuspended reports whether the account is blocked.
func (u *User) IsSuspended() bool {
	return u.SuspendedAt != nil
}

// RefuseDelete is the in-memory half of what the repository does before a delete:
// a protected account stays, and a row with no id cannot have its devices revoked.
func (u *User) RefuseDelete() error {
	if err := u.RefuseIfProtected(); err != nil {
		return err
	}

	if u.ID == uuid.Nil {
		return ErrBatchDeleteUnsupported
	}

	return nil
}
