package files

import (
	"bytes"
	"context"
	"io"
	"sync"

	"github.com/google/uuid"
)

// MemoryBlobs is an in-memory ciphertext store for tests.
type MemoryBlobs struct {
	mu   sync.Mutex
	data map[string][]byte
}

func NewMemoryBlobs() *MemoryBlobs {
	return &MemoryBlobs{data: map[string][]byte{}}
}

var _ Blobs = (*MemoryBlobs)(nil)

func blobKey(orgID, fileID uuid.UUID) string {
	return orgID.String() + "/" + fileID.String()
}

func (m *MemoryBlobs) Put(_ context.Context, orgID, fileID uuid.UUID, ciphertext []byte) (string, error) {
	key := blobKey(orgID, fileID)
	cp := make([]byte, len(ciphertext))
	copy(cp, ciphertext)

	m.mu.Lock()
	m.data[key] = cp
	m.mu.Unlock()

	return key, nil
}

func (m *MemoryBlobs) Open(_ context.Context, orgID, fileID uuid.UUID) (io.ReadCloser, error) {
	key := blobKey(orgID, fileID)

	m.mu.Lock()
	raw, ok := m.data[key]
	m.mu.Unlock()

	if !ok {
		return nil, ErrNotFound
	}

	cp := make([]byte, len(raw))
	copy(cp, raw)

	return io.NopCloser(bytes.NewReader(cp)), nil
}

func (m *MemoryBlobs) Remove(_ context.Context, orgID, fileID uuid.UUID) error {
	key := blobKey(orgID, fileID)

	m.mu.Lock()
	delete(m.data, key)
	m.mu.Unlock()

	return nil
}

func accountKey(fileID uuid.UUID) string {
	return "account/" + fileID.String()
}

func (m *MemoryBlobs) PutAccount(_ context.Context, fileID uuid.UUID, ciphertext []byte) (string, error) {
	key := accountKey(fileID)
	cp := make([]byte, len(ciphertext))
	copy(cp, ciphertext)

	m.mu.Lock()
	m.data[key] = cp
	m.mu.Unlock()

	return key, nil
}

func (m *MemoryBlobs) OpenAccount(_ context.Context, fileID uuid.UUID) (io.ReadCloser, error) {
	key := accountKey(fileID)

	m.mu.Lock()
	raw, ok := m.data[key]
	m.mu.Unlock()

	if !ok {
		return nil, ErrNotFound
	}

	cp := make([]byte, len(raw))
	copy(cp, raw)

	return io.NopCloser(bytes.NewReader(cp)), nil
}

func (m *MemoryBlobs) RemoveAccount(_ context.Context, fileID uuid.UUID) error {
	key := accountKey(fileID)

	m.mu.Lock()
	delete(m.data, key)
	m.mu.Unlock()

	return nil
}
