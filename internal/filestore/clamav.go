package filestore

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/files"
)

// ClamAV talks to clamd over TCP using INSTREAM. There is no client library
// on purpose — the protocol is a handful of length-prefixed chunks.
type ClamAV struct {
	addr    string
	timeout time.Duration
	engine  string
}

func NewClamAV(addr string, timeout time.Duration) *ClamAV {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &ClamAV{addr: addr, timeout: timeout, engine: "clamav"}
}

var _ files.Scanner = (*ClamAV)(nil)

func (c *ClamAV) Scan(ctx context.Context, content []byte) (files.ScanOutcome, error) {
	outcome := files.ScanOutcome{Engine: c.engine}

	dialer := net.Dialer{Timeout: c.timeout}

	conn, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return outcome, fmt.Errorf("%w: %w", files.ErrScanUnavailable, err)
	}
	defer func() { _ = conn.Close() }()

	deadline := time.Now().Add(c.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	if err := conn.SetDeadline(deadline); err != nil {
		return outcome, fmt.Errorf("%w: %w", files.ErrScanUnavailable, err)
	}

	if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return outcome, fmt.Errorf("%w: %w", files.ErrScanUnavailable, err)
	}

	const chunk = 8192
	var size [4]byte

	for off := 0; off < len(content); off += chunk {
		end := off + chunk
		if end > len(content) {
			end = len(content)
		}

		binary.BigEndian.PutUint32(size[:], uint32(end-off))
		if _, err := conn.Write(size[:]); err != nil {
			return outcome, fmt.Errorf("%w: %w", files.ErrScanUnavailable, err)
		}

		if _, err := conn.Write(content[off:end]); err != nil {
			return outcome, fmt.Errorf("%w: %w", files.ErrScanUnavailable, err)
		}
	}

	binary.BigEndian.PutUint32(size[:], 0)
	if _, err := conn.Write(size[:]); err != nil {
		return outcome, fmt.Errorf("%w: %w", files.ErrScanUnavailable, err)
	}

	reply, err := io.ReadAll(io.LimitReader(conn, 1024))
	if err != nil && !errors.Is(err, io.EOF) {
		return outcome, fmt.Errorf("%w: %w", files.ErrScanUnavailable, err)
	}

	text := strings.TrimSpace(string(bytes.Trim(reply, "\x00")))
	switch {
	case strings.Contains(text, "FOUND"):
		return outcome, files.ErrInfected
	case strings.Contains(text, "OK"):
		outcome.Status = files.ScanClean

		return outcome, nil
	default:
		return outcome, fmt.Errorf("%w: unexpected reply %q", files.ErrScanUnavailable, text)
	}
}
