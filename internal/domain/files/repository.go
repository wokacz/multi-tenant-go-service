package files

import (
	"context"
	"errors"
	"io"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

var (
	ErrNotFound = errors.New("files: not found")

	ErrEmpty           = errors.New("files: empty")
	ErrTooLarge        = errors.New("files: too large")
	ErrNameInvalid     = errors.New("files: name is invalid")
	ErrTypeNotAllowed  = errors.New("files: type is not allowed")
	ErrTypeMismatch    = errors.New("files: declared type does not match contents")
	ErrExecutable      = errors.New("files: executable content is blocked")
	ErrInfected        = errors.New("files: content failed the malware scan")
	ErrScanUnavailable = errors.New("files: malware scanner is unavailable")
	ErrCorrupt         = errors.New("files: stored blob cannot be decrypted")
)

// Repository is the organization-scoped persistence this package needs.
//
// Every method takes the organization as its second parameter so a row cannot
// be fetched without naming the tenant that was authorised. TestScopedRepositoryMethodsTakeAnOrganization
// pins that shape. Account-level files (an avatar, later a task attachment
// that is not a tenant document) live on AccountFiles so this interface stays
// free of exceptions.
type Repository interface {
	// File returns ErrNotFound when the row is missing or belongs to another
	// organization.
	File(ctx context.Context, orgID, fileID uuid.UUID) (*ent.File, error)

	Files(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]*ent.File, error)

	// CreateFile persists metadata after the ciphertext is already on disk.
	// It records file.uploaded.
	CreateFile(ctx context.Context, orgID uuid.UUID, file *ent.File) error

	// DeleteFile removes the row. The caller removes the blob. It records
	// file.deleted. ErrNotFound when the row is missing or belongs to another
	// organization.
	DeleteFile(ctx context.Context, orgID, fileID uuid.UUID) error
}

// AccountFiles is the persistence for blobs that are not a tenant document.
// Declared separately from Repository so org-scoped methods stay strictly
// scoped — a task attachment table will hold a file id the same way users
// hold avatar_id.
type AccountFiles interface {
	// CreateAccountFile persists a file with no organization. It does not
	// write the audit log: the caller is acting on their own account.
	CreateAccountFile(ctx context.Context, file *ent.File) error

	// AccountFile returns the file only when it has no organization. An
	// organization file id is indistinguishable from a missing one.
	AccountFile(ctx context.Context, fileID uuid.UUID) (*ent.File, error)

	DeleteAccountFile(ctx context.Context, fileID uuid.UUID) error

	// AttachAvatar points the account at fileID and returns the previous file
	// id (uuid.Nil if none). The previous row is not deleted; the caller
	// removes it.
	AttachAvatar(ctx context.Context, userID, fileID uuid.UUID) (previous uuid.UUID, err error)

	// DetachAvatar clears the pointer and returns the file id that was
	// attached. ErrNotFound when the account has no photo.
	DetachAvatar(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)

	// AvatarFile follows users.avatar_id. ErrNotFound when unset, the account
	// is missing, or the file is an organization document.
	AvatarFile(ctx context.Context, userID uuid.UUID) (*ent.File, error)
}

// Blobs is the ciphertext store. Organization files are keyed by
// (organization, file id); account files by file id under a directory name
// that cannot collide with an organization UUID.
type Blobs interface {
	Put(ctx context.Context, orgID, fileID uuid.UUID, ciphertext []byte) (storageKey string, err error)
	Open(ctx context.Context, orgID, fileID uuid.UUID) (io.ReadCloser, error)
	Remove(ctx context.Context, orgID, fileID uuid.UUID) error

	PutAccount(ctx context.Context, fileID uuid.UUID, ciphertext []byte) (storageKey string, err error)
	OpenAccount(ctx context.Context, fileID uuid.UUID) (io.ReadCloser, error)
	RemoveAccount(ctx context.Context, fileID uuid.UUID) error
}

// Scanner inspects a payload before it is encrypted. A missing scanner is
// represented by NopScanner, not by a nil field — a nil would panic on the
// first upload and look like an application bug.
type Scanner interface {
	Scan(ctx context.Context, content []byte) (ScanOutcome, error)
}

// ScanOutcome is what a successful (or skipped) scan concluded. Infection is
// an error, not a status, because an infected payload is never stored.
type ScanOutcome struct {
	Status string
	Engine string
}

const (
	ScanSkipped     = "skipped"
	ScanClean       = "clean"
	ScanUnavailable = "unavailable"
)
