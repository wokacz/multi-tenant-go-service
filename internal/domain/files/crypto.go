package files

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
)

const (
	envelopeVersion byte = 1
	nonceSize       int  = 12
	keySize         int  = 32
)

// Seal encrypts plaintext with AES-256-GCM. The envelope is
// version || nonce || ciphertext+tag so a key rotation can introduce a
// second version without rereading every blob.
func Seal(key, plaintext []byte) ([]byte, error) {
	aead, err := gcm(key)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("files: nonce: %w", err)
	}

	sealed := aead.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 1+nonceSize+len(sealed))
	out[0] = envelopeVersion
	copy(out[1:], nonce)
	copy(out[1+nonceSize:], sealed)

	return out, nil
}

// Open decrypts an envelope produced by Seal. Tampering, a truncated blob
// or the wrong key all surface as ErrCorrupt — the distinction would leak
// whether the ciphertext parsed, which is not a fact the caller needs.
func Open(key, envelope []byte) ([]byte, error) {
	if len(envelope) < 1+nonceSize+16 {
		return nil, ErrCorrupt
	}

	if envelope[0] != envelopeVersion {
		return nil, ErrCorrupt
	}

	aead, err := gcm(key)
	if err != nil {
		return nil, err
	}

	nonce := envelope[1 : 1+nonceSize]
	plaintext, err := aead.Open(nil, nonce, envelope[1+nonceSize:], nil)
	if err != nil {
		return nil, ErrCorrupt
	}

	return plaintext, nil
}

// KeyID is a short, non-secret identifier for the process key, stored next
// to each blob so a later rotation can find which key still opens it.
func KeyID(key []byte) string {
	sum := sha256.Sum256(key)

	return hex.EncodeToString(sum[:8])
}

func gcm(key []byte) (cipher.AEAD, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("files: encryption key must be %d bytes, got %d", keySize, len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("files: aes: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("files: gcm: %w", err)
	}

	return aead, nil
}
