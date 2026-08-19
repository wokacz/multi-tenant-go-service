package filestore

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/files"
)

func TestClamAVAcceptsACleanStream(t *testing.T) {
	ln := startClamd(t, "stream: OK\x00")
	scanner := NewClamAV(ln.Addr().String(), 0)

	got, err := scanner.Scan(t.Context(), []byte("hello"))
	if err != nil {
		t.Fatalf("Scan() = %v", err)
	}

	if got.Status != files.ScanClean {
		t.Errorf("status = %q, want clean", got.Status)
	}
}

func TestClamAVReportsInfection(t *testing.T) {
	ln := startClamd(t, "stream: Eicar-Test-Signature FOUND\x00")
	scanner := NewClamAV(ln.Addr().String(), 0)

	_, err := scanner.Scan(t.Context(), []byte("eicar"))
	if !errors.Is(err, files.ErrInfected) {
		t.Errorf("Scan() = %v, want ErrInfected", err)
	}
}

func TestClamAVUnavailableWhenNothingListens(t *testing.T) {
	scanner := NewClamAV("127.0.0.1:1", 0)

	_, err := scanner.Scan(t.Context(), []byte("hello"))
	if !errors.Is(err, files.ErrScanUnavailable) {
		t.Errorf("Scan() = %v, want ErrScanUnavailable", err)
	}
}

func startClamd(t *testing.T, reply string) net.Listener {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		buf := make([]byte, 10)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}

		if string(buf) != "zINSTREAM\x00" {
			return
		}

		var size [4]byte
		for {
			if _, err := io.ReadFull(conn, size[:]); err != nil {
				return
			}

			n := binary.BigEndian.Uint32(size[:])
			if n == 0 {
				break
			}

			if _, err := io.CopyN(io.Discard, conn, int64(n)); err != nil {
				return
			}
		}

		_, _ = conn.Write([]byte(reply))
	}()

	return ln
}
