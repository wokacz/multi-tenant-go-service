package memory

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/files"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

var (
	_ files.Repository   = (*Authz)(nil)
	_ files.AccountFiles = (*Authz)(nil)
)

func belongsToOrg(row *ent.File, orgID uuid.UUID) bool {
	return row.OrganizationID != nil && *row.OrganizationID == orgID
}

func (m *Authz) File(_ context.Context, orgID, fileID uuid.UUID) (*ent.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	row, ok := m.files[fileID]
	if !ok || !belongsToOrg(row, orgID) {
		return nil, files.ErrNotFound
	}

	cp := *row

	return &cp, nil
}

func (m *Authz) Files(_ context.Context, orgID uuid.UUID, limit, offset int) ([]*ent.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*ent.File, 0)
	for _, row := range m.files {
		if belongsToOrg(row, orgID) {
			cp := *row
			out = append(out, &cp)
		}
	}

	slices.SortFunc(out, func(a, b *ent.File) int {
		return strings.Compare(b.ID.String(), a.ID.String())
	})

	paged := page(out, limit, offset)
	if paged == nil {
		return []*ent.File{}, nil
	}

	return paged, nil
}

func (m *Authz) CreateFile(ctx context.Context, orgID uuid.UUID, file *ent.File) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if file.ID == uuid.Nil {
		file.ID = uuid.Must(uuid.NewV7())
	}

	now := time.Now().UTC()
	if file.CreatedAt.IsZero() {
		file.CreatedAt = now
	}

	file.UpdatedAt = now
	file.OrganizationID = &orgID

	cp := *file
	m.files[file.ID] = &cp

	m.recordLocked(ctx, ent.AuthzEvent{
		OrganizationID: &orgID,
		SubjectID:      &file.ID,
		Action:         ent.ActionFileUploaded,
		Detail:         file.OriginalName,
	})

	return nil
}

func (m *Authz) DeleteFile(ctx context.Context, orgID, fileID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	row, ok := m.files[fileID]
	if !ok || !belongsToOrg(row, orgID) {
		return files.ErrNotFound
	}

	name := row.OriginalName
	delete(m.files, fileID)

	m.recordLocked(ctx, ent.AuthzEvent{
		OrganizationID: &orgID,
		SubjectID:      &fileID,
		Action:         ent.ActionFileDeleted,
		Detail:         name,
	})

	return nil
}

func (m *Authz) CreateAccountFile(_ context.Context, file *ent.File) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if file.ID == uuid.Nil {
		file.ID = uuid.Must(uuid.NewV7())
	}

	now := time.Now().UTC()
	if file.CreatedAt.IsZero() {
		file.CreatedAt = now
	}

	file.UpdatedAt = now
	file.OrganizationID = nil

	cp := *file
	m.files[file.ID] = &cp

	return nil
}

func (m *Authz) AccountFile(_ context.Context, fileID uuid.UUID) (*ent.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	row, ok := m.files[fileID]
	if !ok || row.OrganizationID != nil {
		return nil, files.ErrNotFound
	}

	cp := *row

	return &cp, nil
}

func (m *Authz) DeleteAccountFile(_ context.Context, fileID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	row, ok := m.files[fileID]
	if !ok || row.OrganizationID != nil {
		return files.ErrNotFound
	}

	delete(m.files, fileID)

	return nil
}

func (m *Authz) AttachAvatar(_ context.Context, userID, fileID uuid.UUID) (uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	row, ok := m.files[fileID]
	if !ok || row.OrganizationID != nil {
		return uuid.Nil, files.ErrNotFound
	}

	if m.users == nil {
		return uuid.Nil, user.ErrNotFound
	}

	return m.users.attachAvatar(userID, fileID)
}

func (m *Authz) DetachAvatar(_ context.Context, userID uuid.UUID) (uuid.UUID, error) {
	if m.users == nil {
		return uuid.Nil, user.ErrNotFound
	}

	return m.users.detachAvatar(userID)
}

func (m *Authz) AvatarFile(ctx context.Context, userID uuid.UUID) (*ent.File, error) {
	if m.users == nil {
		return nil, user.ErrNotFound
	}

	fileID, err := m.users.avatarID(userID)
	if err != nil {
		return nil, err
	}

	return m.AccountFile(ctx, fileID)
}

// SeedFile inserts metadata without going through the upload pipeline, so a
// probe can delete a file that was never sent over HTTP.
func (m *Authz) SeedFile(orgID, fileID, uploadedBy uuid.UUID, name, mediaType string) *ent.File {
	m.mu.Lock()
	defer m.mu.Unlock()

	if fileID == uuid.Nil {
		fileID = uuid.Must(uuid.NewV7())
	}

	now := time.Now().UTC()
	row := &ent.File{
		ID:              fileID,
		CreatedAt:       now,
		UpdatedAt:       now,
		OrganizationID:  &orgID,
		UploadedBy:      uploadedBy,
		OriginalName:    name,
		DetectedType:    mediaType,
		SizeBytes:       1,
		Sha256:          "00",
		StorageKey:      orgID.String() + "/" + fileID.String(),
		EncryptionKeyID: "seed",
		ScanStatus:      ent.FileScanSkipped,
	}
	m.files[fileID] = row

	cp := *row

	return &cp
}
