package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

type fakeRepo struct {
	mu      sync.Mutex
	files   map[uuid.UUID]*ent.File
	avatars map[uuid.UUID]uuid.UUID
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		files:   map[uuid.UUID]*ent.File{},
		avatars: map[uuid.UUID]uuid.UUID{},
	}
}

func belongsToOrg(row *ent.File, orgID uuid.UUID) bool {
	return row.OrganizationID != nil && *row.OrganizationID == orgID
}

func (r *fakeRepo) File(_ context.Context, orgID, fileID uuid.UUID) (*ent.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	row, ok := r.files[fileID]
	if !ok || !belongsToOrg(row, orgID) {
		return nil, ErrNotFound
	}

	cp := *row

	return &cp, nil
}

func (r *fakeRepo) Files(_ context.Context, orgID uuid.UUID, limit, offset int) ([]*ent.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]*ent.File, 0)
	for _, row := range r.files {
		if belongsToOrg(row, orgID) {
			cp := *row
			out = append(out, &cp)
		}
	}

	if offset > len(out) {
		return []*ent.File{}, nil
	}

	out = out[offset:]
	if limit < len(out) {
		out = out[:limit]
	}

	return out, nil
}

func (r *fakeRepo) CreateFile(_ context.Context, orgID uuid.UUID, file *ent.File) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	file.OrganizationID = &orgID
	cp := *file
	r.files[file.ID] = &cp

	return nil
}

func (r *fakeRepo) DeleteFile(_ context.Context, orgID, fileID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	row, ok := r.files[fileID]
	if !ok || !belongsToOrg(row, orgID) {
		return ErrNotFound
	}

	delete(r.files, fileID)

	return nil
}

func (r *fakeRepo) CreateAccountFile(_ context.Context, file *ent.File) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	file.OrganizationID = nil
	cp := *file
	r.files[file.ID] = &cp

	return nil
}

func (r *fakeRepo) AccountFile(_ context.Context, fileID uuid.UUID) (*ent.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	row, ok := r.files[fileID]
	if !ok || row.OrganizationID != nil {
		return nil, ErrNotFound
	}

	cp := *row

	return &cp, nil
}

func (r *fakeRepo) DeleteAccountFile(_ context.Context, fileID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	row, ok := r.files[fileID]
	if !ok || row.OrganizationID != nil {
		return ErrNotFound
	}

	delete(r.files, fileID)

	return nil
}

func (r *fakeRepo) AttachAvatar(_ context.Context, userID, fileID uuid.UUID) (uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	row, ok := r.files[fileID]
	if !ok || row.OrganizationID != nil {
		return uuid.Nil, ErrNotFound
	}

	previous := r.avatars[userID]
	r.avatars[userID] = fileID

	return previous, nil
}

func (r *fakeRepo) DetachAvatar(_ context.Context, userID uuid.UUID) (uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	previous, ok := r.avatars[userID]
	if !ok || previous == uuid.Nil {
		return uuid.Nil, ErrNotFound
	}

	delete(r.avatars, userID)

	return previous, nil
}

func (r *fakeRepo) AvatarFile(ctx context.Context, userID uuid.UUID) (*ent.File, error) {
	r.mu.Lock()
	fileID, ok := r.avatars[userID]
	r.mu.Unlock()

	if !ok || fileID == uuid.Nil {
		return nil, ErrNotFound
	}

	return r.AccountFile(ctx, fileID)
}

type infectedScanner struct{}

func (infectedScanner) Scan(context.Context, []byte) (ScanOutcome, error) {
	return ScanOutcome{Engine: "test"}, ErrInfected
}

func testService(t *testing.T, repo *fakeRepo, scanner Scanner, settings Settings) *Service {
	t.Helper()

	if len(settings.EncryptionKey) == 0 {
		settings.EncryptionKey = bytes.Repeat([]byte{0x11}, 32)
	}

	if scanner == nil {
		scanner = NopScanner()
	}

	svc, err := NewService(repo, NewMemoryBlobs(), repo, scanner, settings)
	if err != nil {
		t.Fatalf("NewService() = %v", err)
	}

	return svc
}

func TestUploadRejectsAnExecutableRegardlessOfTheName(t *testing.T) {
	svc := testService(t, newFakeRepo(), nil, Settings{BlockExecutables: true, MaxBytes: 1 << 20})
	org := uuid.Must(uuid.NewV7())

	_, err := svc.Upload(t.Context(), org, uuid.Must(uuid.NewV7()), "innocent.pdf", "application/pdf", []byte("MZ\x90\x00not-a-pdf"))
	if !errors.Is(err, ErrExecutable) {
		t.Errorf("Upload(mz named pdf) = %v, want ErrExecutable", err)
	}
}

func TestUploadRejectsATypeOutsideTheAllowlist(t *testing.T) {
	svc := testService(t, newFakeRepo(), nil, Settings{
		BlockExecutables: true,
		MaxBytes:         1 << 20,
		AllowedTypes:     []string{"application/pdf"},
	})
	org := uuid.Must(uuid.NewV7())

	_, err := svc.Upload(t.Context(), org, uuid.Must(uuid.NewV7()), "pic.png", "", minimalPNG())
	if !errors.Is(err, ErrTypeNotAllowed) {
		t.Errorf("Upload(png) = %v, want ErrTypeNotAllowed", err)
	}
}

func TestUploadRejectsADeclaredTypeThatDoesNotMatchTheBytes(t *testing.T) {
	svc := testService(t, newFakeRepo(), nil, Settings{
		RequireDeclaredMatch: true,
		MaxBytes:             1 << 20,
	})
	org := uuid.Must(uuid.NewV7())

	_, err := svc.Upload(t.Context(), org, uuid.Must(uuid.NewV7()), "doc.pdf", "application/pdf", minimalPNG())
	if !errors.Is(err, ErrTypeMismatch) {
		t.Errorf("Upload(png declared pdf) = %v, want ErrTypeMismatch", err)
	}
}

func TestUploadStoresCiphertextAndReturnsPlaintextOnRead(t *testing.T) {
	blobs := NewMemoryBlobs()
	repo := newFakeRepo()
	key := bytes.Repeat([]byte{0x22}, 32)

	svc, err := NewService(repo, blobs, repo, NopScanner(), Settings{
		MaxBytes:         1 << 20,
		EncryptionKey:    key,
		BlockExecutables: true,
	})
	if err != nil {
		t.Fatalf("NewService() = %v", err)
	}

	org := uuid.Must(uuid.NewV7())
	actor := uuid.Must(uuid.NewV7())
	png := minimalPNG()

	row, err := svc.Upload(t.Context(), org, actor, "shot.png", "image/png", png)
	if err != nil {
		t.Fatalf("Upload() = %v", err)
	}

	if row.DetectedType != "image/png" {
		t.Errorf("DetectedType = %q, want image/png", row.DetectedType)
	}

	if row.ScanStatus != ent.FileScanSkipped {
		t.Errorf("ScanStatus = %q, want skipped", row.ScanStatus)
	}

	stored, err := blobs.Open(t.Context(), org, row.ID)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	envelope, err := io.ReadAll(stored)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}

	_ = stored.Close()

	if bytes.Contains(envelope, png) {
		t.Fatal("ciphertext contains the plaintext PNG")
	}

	gotRow, plain, err := svc.Content(t.Context(), org, row.ID)
	if err != nil {
		t.Fatalf("Content() = %v", err)
	}

	if gotRow.ID != row.ID {
		t.Errorf("Content id = %v, want %v", gotRow.ID, row.ID)
	}

	if !bytes.Equal(plain, png) {
		t.Error("Content plaintext does not match the upload")
	}

	other := uuid.Must(uuid.NewV7())
	if _, _, err := svc.Content(t.Context(), other, row.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Content(other org) = %v, want ErrNotFound", err)
	}
}

func TestUploadRefusesInfectedContent(t *testing.T) {
	svc := testService(t, newFakeRepo(), infectedScanner{}, Settings{MaxBytes: 1 << 20})
	org := uuid.Must(uuid.NewV7())

	_, err := svc.Upload(t.Context(), org, uuid.Must(uuid.NewV7()), "shot.png", "", minimalPNG())
	if !errors.Is(err, ErrInfected) {
		t.Errorf("Upload(infected) = %v, want ErrInfected", err)
	}
}

func TestUploadRejectsAnOversizedPayload(t *testing.T) {
	svc := testService(t, newFakeRepo(), nil, Settings{MaxBytes: 16})
	org := uuid.Must(uuid.NewV7())

	_, err := svc.Upload(t.Context(), org, uuid.Must(uuid.NewV7()), "tiny.txt", "text/plain", []byte("seventeen bytes!!"))
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("Upload(oversize) = %v, want ErrTooLarge", err)
	}
}

func TestSetAvatarRejectsAPdf(t *testing.T) {
	svc := testService(t, newFakeRepo(), nil, Settings{BlockExecutables: true, AvatarMaxBytes: 1 << 20})
	userID := uuid.Must(uuid.NewV7())

	_, err := svc.SetAvatar(t.Context(), userID, "face.pdf", "application/pdf", []byte("%PDF-1.4\n%"))
	if !errors.Is(err, ErrTypeNotAllowed) {
		t.Errorf("SetAvatar(pdf) = %v, want ErrTypeNotAllowed", err)
	}
}

func TestSetAvatarRejectsAnExecutable(t *testing.T) {
	svc := testService(t, newFakeRepo(), nil, Settings{BlockExecutables: true, AvatarMaxBytes: 1 << 20})
	userID := uuid.Must(uuid.NewV7())

	_, err := svc.SetAvatar(t.Context(), userID, "face.png", "image/png", []byte("MZ\x90\x00not-a-png"))
	if !errors.Is(err, ErrExecutable) {
		t.Errorf("SetAvatar(mz) = %v, want ErrExecutable", err)
	}
}

func TestSetAvatarStoresCiphertextAndReturnsPlaintextOnRead(t *testing.T) {
	blobs := NewMemoryBlobs()
	repo := newFakeRepo()
	key := bytes.Repeat([]byte{0x33}, 32)

	svc, err := NewService(repo, blobs, repo, NopScanner(), Settings{
		AvatarMaxBytes:   1 << 20,
		EncryptionKey:    key,
		BlockExecutables: true,
	})
	if err != nil {
		t.Fatalf("NewService() = %v", err)
	}

	userID := uuid.Must(uuid.NewV7())
	png := minimalPNG()

	meta, err := svc.SetAvatar(t.Context(), userID, "face.png", "image/png", png)
	if err != nil {
		t.Fatalf("SetAvatar() = %v", err)
	}

	if meta.DetectedType != "image/png" {
		t.Errorf("DetectedType = %q, want image/png", meta.DetectedType)
	}

	if meta.OrganizationID != nil {
		t.Errorf("OrganizationID = %v, want nil", meta.OrganizationID)
	}

	stored, err := blobs.OpenAccount(t.Context(), meta.ID)
	if err != nil {
		t.Fatalf("OpenAccount() = %v", err)
	}

	envelope, err := io.ReadAll(stored)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}

	_ = stored.Close()

	if bytes.Contains(envelope, png) {
		t.Fatal("ciphertext contains the plaintext PNG")
	}

	got, plain, err := svc.Avatar(t.Context(), userID)
	if err != nil {
		t.Fatalf("Avatar() = %v", err)
	}

	if got.Sha256 != meta.Sha256 {
		t.Errorf("Sha256 = %q, want %q", got.Sha256, meta.Sha256)
	}

	if !bytes.Equal(plain, png) {
		t.Error("Avatar plaintext does not match the upload")
	}

	other := uuid.Must(uuid.NewV7())
	if _, _, err := svc.Avatar(t.Context(), other); !errors.Is(err, ErrNotFound) {
		t.Errorf("Avatar(other user) = %v, want ErrNotFound", err)
	}

	if err := svc.DeleteAvatar(t.Context(), userID); err != nil {
		t.Fatalf("DeleteAvatar() = %v", err)
	}

	if _, _, err := svc.Avatar(t.Context(), userID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Avatar after delete = %v, want ErrNotFound", err)
	}
}
