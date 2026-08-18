package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testSigner(t *testing.T) *Signer {
	t.Helper()

	s, err := NewSigner("0123456789abcdef0123456789abcdef", time.Hour, testIssuer)
	if err != nil {
		t.Fatalf("NewSigner() = %v", err)
	}

	return s
}

// sign produces a valid signature over signing, so a test can hand Parse a
// well-signed token whose payload it chose. Without it, "the signature is fine
// but the claims are wrong" is indistinguishable from "the signature is wrong".
func sign(t *testing.T, s *Signer, signing string) string {
	t.Helper()

	mac := hmac.New(sha256.New, s.secret)
	if _, err := mac.Write([]byte(signing)); err != nil {
		t.Fatalf("hmac write: %v", err)
	}

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func testSession() Session {
	return Session{
		UserID:   uuid.MustParse("01900000-0000-7000-8000-000000000001"),
		DeviceID: uuid.MustParse("01900000-0000-7000-8000-0000000000d1"),
	}
}

// testIssuer is what the tests sign and verify against. A literal rather than the
// production default, so a test cannot pass because both sides quietly agreed on an
// empty string.
const testIssuer = "test-issuer"

func TestIssueParseRoundTrip(t *testing.T) {
	s := testSigner(t)
	want := testSession()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	token, exp, err := s.Issue(want, now)
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

	if got != want {
		t.Errorf("session = %+v, want %+v", got, want)
	}
}

func TestParseRejectsTampering(t *testing.T) {
	s := testSigner(t)
	now := time.Now().UTC()
	token, _, err := s.Issue(testSession(), now)
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
	token, _, err := s.Issue(testSession(), now)
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
	if _, err := NewSigner("too-short", time.Hour, testIssuer); err == nil {
		t.Fatal("NewSigner() accepted a short secret")
	}
}

func TestIssueRejectsNilUser(t *testing.T) {
	s := testSigner(t)

	sess := testSession()
	sess.UserID = uuid.Nil

	if _, _, err := s.Issue(sess, time.Now().UTC()); err == nil {
		t.Fatal("Issue() signed a nil user id")
	}
}

// TestIssueRejectsNilDevice guards the claim the bearer middleware relies on.
// A token without a device would parse to the zero uuid, and the device check
// would then be looking for a device that cannot exist — which fails closed,
// but only by accident. Refusing to sign one keeps that deliberate.
func TestIssueRejectsNilDevice(t *testing.T) {
	s := testSigner(t)

	sess := testSession()
	sess.DeviceID = uuid.Nil

	if _, _, err := s.Issue(sess, time.Now().UTC()); err == nil {
		t.Fatal("Issue() signed a nil device id")
	}
}

// TestParseRejectsMissingDeviceClaim covers tokens minted before the device
// claim existed. They verify, they have not expired, and they must still be
// refused rather than resolving to the zero device.
func TestParseRejectsMissingDeviceClaim(t *testing.T) {
	s := testSigner(t)
	now := time.Now().UTC()

	// {"sub":"...","exp":9999999999,"iat":1,"ver":0} with no "did".
	legacy := "eyJzdWIiOiIwMTkwMDAwMC0wMDAwLTcwMDAtODAwMC0wMDAwMDAwMDAwMDEiLCJleHAiOjk5OTk5OTk5OTksImlhdCI6MSwidmVyIjowfQ"
	signing := jwtHeader + "." + legacy

	token := signing + "." + sign(t, s, signing)

	if _, err := s.Parse(token, now); err == nil {
		t.Fatal("Parse() accepted a token with no device claim")
	}
}

func TestIssueParsePreservesEpoch(t *testing.T) {
	s := testSigner(t)

	want := testSession()
	want.Epoch = 7
	now := time.Now().UTC()

	token, _, err := s.Issue(want, now)
	if err != nil {
		t.Fatalf("Issue() = %v", err)
	}

	got, err := s.Parse(token, now)
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}

	if got.Epoch != 7 {
		t.Errorf("epoch = %d, want 7", got.Epoch)
	}
}

func TestNewSignerRejectsNonPositiveTTL(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Minute} {
		if _, err := NewSigner("0123456789abcdef0123456789abcdef", ttl, testIssuer); err == nil {
			t.Errorf("NewSigner(ttl=%s) accepted a non-positive TTL", ttl)
		}
	}
}

// TestParseRejectsMalformedTokens walks the shapes a hostile or broken client
// can send. Each has to fail, and all of them have to fail the same way:
// telling "not a JWT" apart from "bad signature" apart from "expired" would let
// a caller probe the verifier one property at a time.
func TestParseRejectsMalformedTokens(t *testing.T) {
	s := testSigner(t)
	now := time.Now().UTC()

	valid, _, err := s.Issue(testSession(), now)
	if err != nil {
		t.Fatalf("Issue() = %v", err)
	}

	parts := strings.Split(valid, ".")

	// Well-signed envelopes carrying payloads the parser must still refuse.
	signed := func(payload string) string {
		signing := jwtHeader + "." + base64.RawURLEncoding.EncodeToString([]byte(payload))

		return signing + "." + sign(t, s, signing)
	}

	cases := map[string]string{
		"empty":                "",
		"one part":             parts[0],
		"two parts":            parts[0] + "." + parts[1],
		"four parts":           valid + ".extra",
		"header not base64":    "!!!." + parts[1] + "." + parts[2],
		"payload not base64":   parts[0] + ".!!!." + parts[2],
		"signature not base64": parts[0] + "." + parts[1] + ".!!!",
		"payload not json":     signed("this is not json"),
		"subject not a uuid":   signed(`{"sub":"nope","exp":9999999999,"iat":1,"ver":0,"did":"01900000-0000-7000-8000-0000000000d1"}`),
		"nil subject":          signed(`{"sub":"00000000-0000-0000-0000-000000000000","exp":9999999999,"iat":1,"ver":0,"did":"01900000-0000-7000-8000-0000000000d1"}`),
		"device not a uuid":    signed(`{"sub":"01900000-0000-7000-8000-000000000001","exp":9999999999,"iat":1,"ver":0,"did":"nope"}`),
	}

	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Parse(token, now); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("Parse() = %v, want ErrInvalidToken", err)
			}
		})
	}
}

// TestParseRejectsAForeignSecret is the property the whole scheme rests on.
func TestParseRejectsAForeignSecret(t *testing.T) {
	mine := testSigner(t)

	theirs, err := NewSigner("ffffffffffffffffffffffffffffffff", time.Hour, testIssuer)
	if err != nil {
		t.Fatalf("NewSigner() = %v", err)
	}

	now := time.Now().UTC()

	token, _, err := theirs.Issue(testSession(), now)
	if err != nil {
		t.Fatalf("Issue() = %v", err)
	}

	if _, err := mine.Parse(token, now); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Parse() = %v, want ErrInvalidToken", err)
	}
}

// TestParseRejectsAnotherInstallationSharingTheSecret is why iss and aud exist.
//
// The secret is the same, so the signature verifies — that is the whole point of
// the case. Two deployments of this product configured from the same secrets file
// is not a hypothetical, and without these claims a staging token is a production
// token.
func TestParseRejectsAnotherInstallationSharingTheSecret(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"

	staging, err := NewSigner(secret, time.Hour, "staging")
	if err != nil {
		t.Fatalf("NewSigner() = %v", err)
	}

	production, err := NewSigner(secret, time.Hour, "production")
	if err != nil {
		t.Fatalf("NewSigner() = %v", err)
	}

	now := time.Now().UTC()

	token, _, err := staging.Issue(testSession(), now)
	if err != nil {
		t.Fatalf("Issue() = %v", err)
	}

	// The signature is good; only the issuer is wrong.
	if _, err := staging.Parse(token, now); err != nil {
		t.Fatalf("the issuer cannot verify its own token: %v", err)
	}

	if _, err := production.Parse(token, now); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("Parse() = %v, want ErrInvalidToken", err)
	}
}

// TestParseRejectsATokenFromBeforeTheClaimsExisted is the deliberate cost of the
// change.
//
// A token without iss and aud is a token this build did not issue, and tolerating
// one for a grace period would mean carrying a code path nobody removes and a
// verification that is optional in the meantime. Every session ends once, which is
// one sign-in.
func TestParseRejectsATokenFromBeforeTheClaimsExisted(t *testing.T) {
	s := testSigner(t)
	now := time.Now().UTC()

	// The old shape: sub, exp, iat, ver, did and nothing else, correctly signed.
	payload := fmt.Sprintf(
		`{"sub":%q,"exp":%d,"iat":%d,"ver":0,"did":%q}`,
		testSession().UserID, now.Add(time.Hour).Unix(), now.Unix(), testSession().DeviceID)

	signing := jwtHeader + "." + base64.RawURLEncoding.EncodeToString([]byte(payload))
	token := signing + "." + sign(t, s, signing)

	if _, err := s.Parse(token, now); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("Parse() = %v, want ErrInvalidToken", err)
	}
}
