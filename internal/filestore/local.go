package filestore

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/files"
)

// Local stores ciphertext on disk. Organization files live at
// {root}/{orgID}/{fileID}; account files at {root}/account/{fileID}. The
// directory name "account" cannot collide with an organization UUID, and the
// remaining names are UUIDs, so a crafted original filename cannot walk out of
// the root.
type Local struct {
	root string
}

func NewLocal(root string) (*Local, error) {
	if root == "" {
		return nil, fmt.Errorf("filestore: storage path is empty")
	}

	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("filestore: create %s: %w", root, err)
	}

	return &Local{root: root}, nil
}

var _ files.Blobs = (*Local)(nil)

func (l *Local) path(orgID, fileID uuid.UUID) string {
	return filepath.Join(l.root, orgID.String(), fileID.String())
}

func (l *Local) Put(_ context.Context, orgID, fileID uuid.UUID, ciphertext []byte) (string, error) {
	dir := filepath.Join(l.root, orgID.String())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("filestore: mkdir: %w", err)
	}

	path := l.path(orgID, fileID)
	tmp := path + ".tmp"

	if err := os.WriteFile(tmp, ciphertext, 0o600); err != nil {
		return "", fmt.Errorf("filestore: write: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)

		return "", fmt.Errorf("filestore: rename: %w", err)
	}

	return orgID.String() + "/" + fileID.String(), nil
}

func (l *Local) Open(_ context.Context, orgID, fileID uuid.UUID) (io.ReadCloser, error) {
	f, err := os.Open(l.path(orgID, fileID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, files.ErrNotFound
		}

		return nil, fmt.Errorf("filestore: open: %w", err)
	}

	return f, nil
}

func (l *Local) Remove(_ context.Context, orgID, fileID uuid.UUID) error {
	err := os.Remove(l.path(orgID, fileID))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("filestore: remove: %w", err)
	}

	return nil
}

func (l *Local) accountPath(fileID uuid.UUID) string {
	return filepath.Join(l.root, "account", fileID.String())
}

func (l *Local) PutAccount(_ context.Context, fileID uuid.UUID, ciphertext []byte) (string, error) {
	dir := filepath.Join(l.root, "account")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("filestore: mkdir: %w", err)
	}

	path := l.accountPath(fileID)
	tmp := path + ".tmp"

	if err := os.WriteFile(tmp, ciphertext, 0o600); err != nil {
		return "", fmt.Errorf("filestore: write: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)

		return "", fmt.Errorf("filestore: rename: %w", err)
	}

	return "account/" + fileID.String(), nil
}

func (l *Local) OpenAccount(_ context.Context, fileID uuid.UUID) (io.ReadCloser, error) {
	f, err := os.Open(l.accountPath(fileID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, files.ErrNotFound
		}

		return nil, fmt.Errorf("filestore: open: %w", err)
	}

	return f, nil
}

func (l *Local) RemoveAccount(_ context.Context, fileID uuid.UUID) error {
	err := os.Remove(l.accountPath(fileID))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("filestore: remove: %w", err)
	}

	return nil
}
