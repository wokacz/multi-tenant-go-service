package repositories_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/files"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories"
)

func TestFileIsScopedToTheOrganization(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewFiles(db)
	org := newOrganization(t, db)
	other := newOrganization(t, db)

	row := &ent.File{
		UploadedBy:      uuid.Must(uuid.NewV7()),
		OriginalName:    "shot.png",
		DetectedType:    "image/png",
		SizeBytes:       1,
		Sha256:          strings.Repeat("ab", 32),
		StorageKey:      "org/file",
		EncryptionKeyID: "seed",
		ScanStatus:      ent.FileScanSkipped,
	}
	if err := repo.CreateFile(t.Context(), org.ID, row); err != nil {
		t.Fatalf("CreateFile() = %v", err)
	}

	got, err := repo.File(t.Context(), org.ID, row.ID)
	if err != nil {
		t.Fatalf("File() = %v", err)
	}

	if got.OriginalName != "shot.png" {
		t.Errorf("OriginalName = %q, want shot.png", got.OriginalName)
	}

	if _, err := repo.File(t.Context(), other.ID, row.ID); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("File(other org) = %v, want ErrNotFound", err)
	}

	listed, err := repo.Files(t.Context(), other.ID, 100, 0)
	if err != nil {
		t.Fatalf("Files(other org) = %v", err)
	}

	if len(listed) != 0 {
		t.Errorf("Files(other org) returned %d rows, want 0", len(listed))
	}

	if err := repo.DeleteFile(t.Context(), other.ID, row.ID); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("DeleteFile(other org) = %v, want ErrNotFound", err)
	}

	if err := repo.DeleteFile(t.Context(), org.ID, row.ID); err != nil {
		t.Fatalf("DeleteFile() = %v", err)
	}

	if _, err := repo.File(t.Context(), org.ID, row.ID); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("File after delete = %v, want ErrNotFound", err)
	}
}

func TestAccountFileIsInvisibleToOrganizationQueries(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewFiles(db)
	org := newOrganization(t, db)
	users := repositories.NewUser(db)
	u := newUser(t, users)

	row := &ent.File{
		UploadedBy:      u.ID,
		OriginalName:    "face.png",
		DetectedType:    "image/png",
		SizeBytes:       1,
		Sha256:          strings.Repeat("cd", 32),
		StorageKey:      "account/file",
		EncryptionKeyID: "seed",
		ScanStatus:      ent.FileScanSkipped,
	}
	if err := repo.CreateAccountFile(t.Context(), row); err != nil {
		t.Fatalf("CreateAccountFile() = %v", err)
	}

	if row.OrganizationID != nil {
		t.Errorf("OrganizationID = %v, want nil", row.OrganizationID)
	}

	listed, err := repo.Files(t.Context(), org.ID, 100, 0)
	if err != nil {
		t.Fatalf("Files() = %v", err)
	}

	if len(listed) != 0 {
		t.Errorf("Files(org) returned %d rows, want 0", len(listed))
	}

	if _, err := repo.File(t.Context(), org.ID, row.ID); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("File(org, account file) = %v, want ErrNotFound", err)
	}

	got, err := repo.AccountFile(t.Context(), row.ID)
	if err != nil {
		t.Fatalf("AccountFile() = %v", err)
	}

	if got.ID != row.ID {
		t.Errorf("AccountFile id = %v, want %v", got.ID, row.ID)
	}
}

func TestAttachAvatarPointsTheAccountAtTheFile(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewFiles(db)
	users := repositories.NewUser(db)
	u := newUser(t, users)
	other := newUser(t, users)

	row := &ent.File{
		UploadedBy:      u.ID,
		OriginalName:    "face.png",
		DetectedType:    "image/png",
		SizeBytes:       42,
		Sha256:          strings.Repeat("ab", 32),
		StorageKey:      "account/file",
		EncryptionKeyID: "seed",
		ScanStatus:      ent.FileScanSkipped,
	}
	if err := repo.CreateAccountFile(t.Context(), row); err != nil {
		t.Fatalf("CreateAccountFile() = %v", err)
	}

	previous, err := repo.AttachAvatar(t.Context(), u.ID, row.ID)
	if err != nil {
		t.Fatalf("AttachAvatar() = %v", err)
	}

	if previous != uuid.Nil {
		t.Errorf("previous = %v, want nil", previous)
	}

	got, err := repo.AvatarFile(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("AvatarFile() = %v", err)
	}

	if got.ID != row.ID {
		t.Errorf("AvatarFile id = %v, want %v", got.ID, row.ID)
	}

	if _, err := repo.AvatarFile(t.Context(), other.ID); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("AvatarFile(other) = %v, want ErrNotFound", err)
	}

	account, err := users.ByID(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}

	if account.SessionEpoch != u.SessionEpoch {
		t.Errorf("session epoch moved to %d; a photo is not a credential change",
			account.SessionEpoch)
	}

	if _, err := repo.AttachAvatar(t.Context(), uuid.Must(uuid.NewV7()), row.ID); !errors.Is(err, user.ErrNotFound) {
		t.Errorf("AttachAvatar() on an unknown id = %v, want user.ErrNotFound", err)
	}

	detached, err := repo.DetachAvatar(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("DetachAvatar() = %v", err)
	}

	if detached != row.ID {
		t.Errorf("DetachAvatar() = %v, want %v", detached, row.ID)
	}

	if _, err := repo.AvatarFile(t.Context(), u.ID); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("AvatarFile after detach = %v, want ErrNotFound", err)
	}
}
