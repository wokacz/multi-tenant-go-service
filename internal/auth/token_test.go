package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testSigner(t *testing.T) *Signer {
	t.Helper()

	s, err := NewSigner("0123456789abcdef0123456789abcdef", time.Hour)
	if err != nil {
		t.Fatalf("NewSigner() = %v", err)
	}

	return s
}

func TestIssueParseRoundTrip(t *testing.T) {
	s := testSigner(t)
	id := uuid.MustParse("01900000-0000-7000-8000-000000000001")
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	token, exp, err := s.Issue(id, now)
	if err != nil {
		t.Fatalf("Issue() = %v", err)
	}

	if !exp.Equal(now.Add(time.Hour)) {
		t.Errorf("expires = %s, want %s", exp, now.Add(time.Hour))
	}

	got, err := s.Parse(token, now)
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}

	if got != id {
		t.Errorf("subject = %s, want %s", got, id)
	}
}

func TestParseRejectsTampering(t *testing.T) {
	s := testSigner(t)
	now := time.Now().UTC()
	token, _, err := s.Issue(uuid.Must(uuid.NewV7()), now)
	if err != nil {
		t.Fatalf("Issue() = %v", err)
	}

	parts := strings.Split(token, ".")
	tampered := parts[0] + "." + parts[1] + "." + strings.Repeat("A", len(parts[2]))

	if _, err := s.Parse(tampered, now); err == nil {
		t.Fatal("Parse() accepted a tampered signature")
	}
}

func TestParseRejectsExpired(t *testing.T) {
	s := testSigner(t)
	now := time.Now().UTC()
	token, _, err := s.Issue(uuid.Must(uuid.NewV7()), now)
	if err != nil {
		t.Fatalf("Issue() = %v", err)
	}

	if _, err := s.Parse(token, now.Add(2*time.Hour)); err == nil {
		t.Fatal("Parse() accepted an expired token")
	}
}

func TestParseRejectsAlgNoneHeader(t *testing.T) {
	s := testSigner(t)
	noneHeader := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0" // {"alg":"none","typ":"JWT"}
	payload := "eyJzdWIiOiIwMTkwMDAwMC0wMDAwLTcwMDAtODAwMC0wMDAwMDAwMDAwMDEiLCJleHAiOjk5OTk5OTk5OTksImlhdCI6MX0"
	token := noneHeader + "." + payload + "."

	if _, err := s.Parse(token, time.Now().UTC()); err == nil {
		t.Fatal("Parse() accepted alg=none")
	}
}

func TestNewSignerRejectsShortSecret(t *testing.T) {
	if _, err := NewSigner("too-short", time.Hour); err == nil {
		t.Fatal("NewSigner() accepted a short secret")
	}
}

func TestIssueRejectsNilUser(t *testing.T) {
	s := testSigner(t)
	if _, _, err := s.Issue(uuid.Nil, time.Now().UTC()); err == nil {
		t.Fatal("Issue() signed a nil user id")
	}
}
