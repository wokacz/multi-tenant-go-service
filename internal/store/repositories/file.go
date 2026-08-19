package repositories

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/files"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	entfile "github.com/wokacz/multi-tenant-go-service/internal/store/ent/file"
	entuser "github.com/wokacz/multi-tenant-go-service/internal/store/ent/user"
)

// Files implements files.Repository.
type Files struct {
	db *store.DB
}

func NewFiles(db *store.DB) *Files {
	return &Files{db: db}
}

var _ files.Repository = (*Files)(nil)
var _ files.AccountFiles = (*Files)(nil)

func (r *Files) withTx(ctx context.Context, fn func(tx *ent.Tx) error) error {
	return withEntTx(ctx, r.db.Ent(), fn)
}

func translateFileError(op string, err error) error {
	switch {
	case err == nil:
		return nil
	case isNotFound(err):
		return files.ErrNotFound
	case isForeignKeyViolation(err):
		return files.ErrNotFound
	default:
		return fmt.Errorf("store: %s: %w", op, err)
	}
}

func (r *Files) File(ctx context.Context, orgID, fileID uuid.UUID) (*ent.File, error) {
	row, err := r.db.Ent().File.Query().
		Where(entfile.ID(fileID), entfile.OrganizationID(orgID)).
		Only(ctx)
	if err != nil {
		return nil, translateFileError("file", err)
	}

	return row, nil
}

func (r *Files) Files(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]*ent.File, error) {
	rows, err := r.db.Ent().File.Query().
		Where(entfile.OrganizationID(orgID)).
		Order(ent.Desc(entfile.FieldID)).
		Limit(limit).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, translateFileError("files", err)
	}

	if rows == nil {
		rows = []*ent.File{}
	}

	return rows, nil
}

func (r *Files) CreateFile(ctx context.Context, orgID uuid.UUID, file *ent.File) error {
	file.OrganizationID = &orgID

	err := r.withTx(ctx, func(tx *ent.Tx) error {
		create := tx.File.Create().
			SetOrganizationID(orgID).
			SetUploadedBy(file.UploadedBy).
			SetOriginalName(file.OriginalName).
			SetDeclaredType(file.DeclaredType).
			SetDetectedType(file.DetectedType).
			SetSizeBytes(file.SizeBytes).
			SetSha256(file.Sha256).
			SetStorageKey(file.StorageKey).
			SetEncryptionKeyID(file.EncryptionKeyID).
			SetScanStatus(file.ScanStatus).
			SetScanEngine(file.ScanEngine)
		if file.ID != uuid.Nil {
			create = create.SetID(file.ID)
		}

		created, err := create.Save(ctx)
		if err != nil {
			return err
		}

		file.ID = created.ID
		file.CreatedAt = created.CreatedAt
		file.UpdatedAt = created.UpdatedAt

		return recordEnt(ctx, tx, &ent.AuthzEvent{
			OrganizationID: &orgID,
			SubjectID:      &file.ID,
			Action:         ent.ActionFileUploaded,
			Detail:         file.OriginalName,
		})
	})

	return translateFileError("create file", err)
}

func (r *Files) DeleteFile(ctx context.Context, orgID, fileID uuid.UUID) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		row, err := tx.File.Query().
			Where(entfile.ID(fileID), entfile.OrganizationID(orgID)).
			Only(ctx)
		if err != nil {
			return err
		}

		if err := tx.File.DeleteOneID(row.ID).Exec(ctx); err != nil {
			return err
		}

		return recordEnt(ctx, tx, &ent.AuthzEvent{
			OrganizationID: &orgID,
			SubjectID:      &fileID,
			Action:         ent.ActionFileDeleted,
			Detail:         row.OriginalName,
		})
	})

	return translateFileError("delete file", err)
}

func (r *Files) CreateAccountFile(ctx context.Context, file *ent.File) error {
	file.OrganizationID = nil

	create := r.db.Ent().File.Create().
		SetUploadedBy(file.UploadedBy).
		SetOriginalName(file.OriginalName).
		SetDeclaredType(file.DeclaredType).
		SetDetectedType(file.DetectedType).
		SetSizeBytes(file.SizeBytes).
		SetSha256(file.Sha256).
		SetStorageKey(file.StorageKey).
		SetEncryptionKeyID(file.EncryptionKeyID).
		SetScanStatus(file.ScanStatus).
		SetScanEngine(file.ScanEngine)
	if file.ID != uuid.Nil {
		create = create.SetID(file.ID)
	}

	created, err := create.Save(ctx)
	if err != nil {
		return translateFileError("create account file", err)
	}

	file.ID = created.ID
	file.CreatedAt = created.CreatedAt
	file.UpdatedAt = created.UpdatedAt

	return nil
}

func (r *Files) AccountFile(ctx context.Context, fileID uuid.UUID) (*ent.File, error) {
	row, err := r.db.Ent().File.Query().
		Where(entfile.ID(fileID), entfile.OrganizationIDIsNil()).
		Only(ctx)
	if err != nil {
		return nil, translateFileError("account file", err)
	}

	return row, nil
}

func (r *Files) DeleteAccountFile(ctx context.Context, fileID uuid.UUID) error {
	n, err := r.db.Ent().File.Delete().
		Where(entfile.ID(fileID), entfile.OrganizationIDIsNil()).
		Exec(ctx)
	if err != nil {
		return translateFileError("delete account file", err)
	}

	if n == 0 {
		return files.ErrNotFound
	}

	return nil
}

func (r *Files) AttachAvatar(ctx context.Context, userID, fileID uuid.UUID) (uuid.UUID, error) {
	var previous uuid.UUID

	err := r.withTx(ctx, func(tx *ent.Tx) error {
		u, err := tx.User.Query().
			Where(entuser.ID(userID), entuser.DeletedAtIsNil()).
			Only(ctx)
		if err != nil {
			if isNotFound(err) {
				return user.ErrNotFound
			}

			return err
		}

		if _, err := tx.File.Query().
			Where(entfile.ID(fileID), entfile.OrganizationIDIsNil()).
			Only(ctx); err != nil {
			return err
		}

		if u.AvatarID != nil {
			previous = *u.AvatarID
		}

		affected, err := tx.User.Update().
			Where(entuser.ID(userID), entuser.DeletedAtIsNil()).
			SetAvatarID(fileID).
			Save(ctx)
		if err != nil {
			return err
		}

		if affected == 0 {
			return user.ErrNotFound
		}

		return nil
	})

	if err != nil {
		if errors.Is(err, user.ErrNotFound) {
			return uuid.Nil, err
		}

		return uuid.Nil, translateFileError("attach avatar", err)
	}

	return previous, nil
}

func (r *Files) DetachAvatar(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	u, err := r.db.Ent().User.Query().
		Where(entuser.ID(userID), entuser.DeletedAtIsNil()).
		Only(ctx)
	if err != nil {
		if isNotFound(err) {
			return uuid.Nil, user.ErrNotFound
		}

		return uuid.Nil, translateFileError("detach avatar", err)
	}

	if u.AvatarID == nil {
		return uuid.Nil, files.ErrNotFound
	}

	previous := *u.AvatarID

	affected, err := r.db.Ent().User.Update().
		Where(entuser.ID(userID), entuser.DeletedAtIsNil()).
		ClearAvatarID().
		Save(ctx)
	if err != nil {
		return uuid.Nil, translateFileError("detach avatar", err)
	}

	if affected == 0 {
		return uuid.Nil, user.ErrNotFound
	}

	return previous, nil
}

func (r *Files) AvatarFile(ctx context.Context, userID uuid.UUID) (*ent.File, error) {
	u, err := r.db.Ent().User.Query().
		Where(entuser.ID(userID), entuser.DeletedAtIsNil()).
		Only(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil, user.ErrNotFound
		}

		return nil, translateFileError("avatar file", err)
	}

	if u.AvatarID == nil {
		return nil, files.ErrNotFound
	}

	return r.AccountFile(ctx, *u.AvatarID)
}
