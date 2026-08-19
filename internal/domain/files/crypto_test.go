package files

import (
	"bytes"
	"errors"
	"testing"
)

func TestSealRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	plain := []byte("tenant document")

	envelope, err := Seal(key, plain)
	if err != nil {
		t.Fatalf("Seal() = %v", err)
	}

	got, err := Open(key, envelope)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	if !bytes.Equal(got, plain) {
		t.Errorf("Open() = %q, want %q", got, plain)
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	envelope, err := Seal(key, []byte("secret"))
	if err != nil {
		t.Fatalf("Seal() = %v", err)
	}

	envelope[len(envelope)-1] ^= 0xff

	if _, err := Open(key, envelope); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Open(tampered) = %v, want ErrCorrupt", err)
	}
}

func TestOpenRejectsTheWrongKey(t *testing.T) {
	envelope, err := Seal(bytes.Repeat([]byte{0x01}, 32), []byte("secret"))
	if err != nil {
		t.Fatalf("Seal() = %v", err)
	}

	if _, err := Open(bytes.Repeat([]byte{0x02}, 32), envelope); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Open(wrong key) = %v, want ErrCorrupt", err)
	}
}

func TestKeyIDIsStableAndNotTheKey(t *testing.T) {
	key := bytes.Repeat([]byte{0x07}, 32)
	id := KeyID(key)
	if id != KeyID(key) {
		t.Fatal("KeyID is not stable")
	}

	if bytes.Contains([]byte(id), key) {
		t.Fatal("KeyID contains the key")
	}

	if len(id) != 16 {
		t.Errorf("KeyID length = %d, want 16 hex characters of an 8-byte prefix", len(id))
	}
}
