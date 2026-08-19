package filestore

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/files"
)

func TestLocalRoundTripAndIsolation(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal() = %v", err)
	}

	orgA := uuid.Must(uuid.NewV7())
	orgB := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())
	payload := []byte("ciphertext")

	key, err := store.Put(t.Context(), orgA, id, payload)
	if err != nil {
		t.Fatalf("Put() = %v", err)
	}

	if key != orgA.String()+"/"+id.String() {
		t.Errorf("storage key = %q", key)
	}

	rc, err := store.Open(t.Context(), orgA, id)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if err := rc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if !bytes.Equal(got, payload) {
		t.Errorf("Open() = %q, want %q", got, payload)
	}

	if _, err := store.Open(t.Context(), orgB, id); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("Open(other org) = %v, want ErrNotFound", err)
	}

	info, err := os.Stat(filepath.Join(root, orgA.String(), id.String()))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
	}

	if err := store.Remove(t.Context(), orgA, id); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	if _, err := store.Open(t.Context(), orgA, id); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("Open after Remove = %v, want ErrNotFound", err)
	}
}

func TestAccountBlobIsIsolatedFromOrganizationBlobs(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal() = %v", err)
	}

	orgID := uuid.Must(uuid.NewV7())
	fileID := uuid.Must(uuid.NewV7())
	accountID := uuid.Must(uuid.NewV7())

	if _, err := store.Put(t.Context(), orgID, fileID, []byte("org-blob")); err != nil {
		t.Fatalf("Put() = %v", err)
	}

	if _, err := store.PutAccount(t.Context(), accountID, []byte("avatar-blob")); err != nil {
		t.Fatalf("PutAccount() = %v", err)
	}

	if _, err := store.Open(t.Context(), orgID, accountID); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("Open(org, accountID) = %v, want ErrNotFound", err)
	}

	rc, err := store.OpenAccount(t.Context(), accountID)
	if err != nil {
		t.Fatalf("OpenAccount() = %v", err)
	}

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if err := rc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if !bytes.Equal(got, []byte("avatar-blob")) {
		t.Errorf("OpenAccount() = %q, want avatar-blob", got)
	}

	info, err := os.Stat(filepath.Join(root, "account", accountID.String()))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
	}

	if err := store.RemoveAccount(t.Context(), accountID); err != nil {
		t.Fatalf("RemoveAccount() = %v", err)
	}

	if _, err := store.OpenAccount(t.Context(), accountID); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("OpenAccount after Remove = %v, want ErrNotFound", err)
	}

	if _, err := store.Open(t.Context(), orgID, fileID); err != nil {
		t.Errorf("organization blob was removed with the account file: %v", err)
	}
}
