package user_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

const (
	testEmail    = "ada@example.com"
	testPassword = "twelve-chars"
)

func laptop(deviceToken string) user.SignInContext {
	return user.SignInContext{IP: "192.0.2.10", UserAgent: "test-agent", DeviceToken: deviceToken}
}

func register(t *testing.T, s *user.Service) {
	t.Helper()

	if _, err := s.Create(context.Background(), "Ada", testEmail, testPassword, testPassword, ""); err != nil {
		t.Fatalf("Create() = %v", err)
	}
}

func signIn(t *testing.T, s *user.Service, deviceToken string) *user.SignInResult {
	t.Helper()

	result, err := s.SignIn(context.Background(), testEmail, testPassword, laptop(deviceToken))
	if err != nil {
		t.Fatalf("SignIn() = %v", err)
	}

	return result
}

func TestSignInMintsADeviceOnceAndReusesIt(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	first := signIn(t, s, "")
	if first.DeviceToken == "" {
		t.Fatal("first sign-in returned no device token")
	}

	if first.Challenged {
		t.Fatal("two-factor is off, sign-in should not have been challenged")
	}

	second := signIn(t, s, first.DeviceToken)
	if second.DeviceToken != "" {
		t.Error("a recognised device should not be handed a new token")
	}

	if second.Device.ID != first.Device.ID {
		t.Errorf("device = %s, want the one from the first sign-in %s", second.Device.ID, first.Device.ID)
	}
}

// TestSignInWithAnotherAccountsDeviceTokenStartsFresh guards the user scoping
// on the fingerprint lookup. A token lifted from someone else must not resolve
// to their device, and must not be an error either — it looks exactly like a
// client whose database was reset.
func TestSignInWithAnotherAccountsDeviceTokenStartsFresh(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	if _, err := s.Create(context.Background(), "Bob", "bob@example.com", testPassword, testPassword, ""); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	stolen, err := s.SignIn(context.Background(), "bob@example.com", testPassword, laptop(""))
	if err != nil {
		t.Fatalf("SignIn(bob) = %v", err)
	}

	mine := signIn(t, s, stolen.DeviceToken)
	if mine.DeviceToken == "" {
		t.Fatal("an unrecognised token should mint a new device")
	}

	if mine.Device.ID == stolen.Device.ID {
		t.Fatal("resolved another account's device")
	}
}

func TestSignInRecordsHistory(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	ctx := context.Background()
	first := signIn(t, s, "")

	if _, err := s.SignIn(ctx, testEmail, "wrong-password", laptop(first.DeviceToken)); !errors.Is(err, user.ErrInvalidCredentials) {
		t.Fatalf("SignIn(wrong password) = %v, want ErrInvalidCredentials", err)
	}

	events, err := s.LoginEvents(ctx, first.User.ID, 10)
	if err != nil {
		t.Fatalf("LoginEvents() = %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}

	if events[0].Outcome != ent.OutcomeBadPassword {
		t.Errorf("newest outcome = %q, want bad_password", events[0].Outcome)
	}

	if events[1].Outcome != ent.OutcomeSuccess {
		t.Errorf("oldest outcome = %q, want success", events[1].Outcome)
	}

	if events[0].IP != "192.0.2.10" {
		t.Errorf("ip = %q, want 192.0.2.10", events[0].IP)
	}
}

// TestSignInDoesNotRecordUnknownAddresses is the other half of the enumeration
// story. Nothing is written for an address that is not registered, so the
// history a real account shows cannot be padded by an outsider.
func TestSignInDoesNotRecordUnknownAddresses(t *testing.T) {
	s, repo := testService(t)
	register(t, s)

	ctx := context.Background()

	if _, err := s.SignIn(ctx, "nobody@example.com", testPassword, laptop("")); !errors.Is(err, user.ErrInvalidCredentials) {
		t.Fatalf("SignIn(unknown) = %v, want ErrInvalidCredentials", err)
	}

	u, err := repo.ByEmail(ctx, testEmail)
	if err != nil {
		t.Fatalf("ByEmail() = %v", err)
	}

	events, err := s.LoginEvents(ctx, u.ID, 10)
	if err != nil {
		t.Fatalf("LoginEvents() = %v", err)
	}

	if len(events) != 0 {
		t.Fatalf("events = %d, want 0", len(events))
	}
}

func TestRevokedDeviceCannotSignIn(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	ctx := context.Background()
	first := signIn(t, s, "")

	if err := s.RevokeDevice(ctx, first.User.ID, first.Device.ID); err != nil {
		t.Fatalf("RevokeDevice() = %v", err)
	}

	if _, err := s.SignIn(ctx, testEmail, testPassword, laptop(first.DeviceToken)); !errors.Is(err, user.ErrDeviceRevoked) {
		t.Fatalf("SignIn(revoked) = %v, want ErrDeviceRevoked", err)
	}

	// Revoking is idempotent, so a client retrying does not get an error it
	// has to special-case.
	if err := s.RevokeDevice(ctx, first.User.ID, first.Device.ID); err != nil {
		t.Fatalf("RevokeDevice(twice) = %v", err)
	}

	if _, err := s.ActiveDevice(ctx, first.User.ID, first.Device.ID); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("ActiveDevice(revoked) = %v, want ErrNotFound", err)
	}
}

func TestRevokeDeviceRejectsAnotherAccountsDevice(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	ctx := context.Background()
	mine := signIn(t, s, "")

	if _, err := s.Create(ctx, "Bob", "bob@example.com", testPassword, testPassword, ""); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	theirs, err := s.SignIn(ctx, "bob@example.com", testPassword, laptop(""))
	if err != nil {
		t.Fatalf("SignIn(bob) = %v", err)
	}

	if err := s.RevokeDevice(ctx, mine.User.ID, theirs.Device.ID); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("RevokeDevice(other account) = %v, want ErrNotFound", err)
	}
}

func TestDevicesListsNewestFirst(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	ctx := context.Background()
	first := signIn(t, s, "")
	second := signIn(t, s, "")

	devices, err := s.Devices(ctx, first.User.ID)
	if err != nil {
		t.Fatalf("Devices() = %v", err)
	}

	if len(devices) != 2 {
		t.Fatalf("devices = %d, want 2", len(devices))
	}

	if devices[0].ID != second.Device.ID {
		t.Errorf("first listed = %s, want the most recently seen %s", devices[0].ID, second.Device.ID)
	}

	// The token itself is never stored, only its digest.
	for _, device := range devices {
		if device.Fingerprint == first.DeviceToken || device.Fingerprint == second.DeviceToken {
			t.Fatal("a device token was stored verbatim")
		}
	}
}

// TestLoginEventsClampsTheLimit covers the guard on response size. A caller
// asking for everything, or for nothing, gets the documented maximum.
func TestLoginEventsClampsTheLimit(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	ctx := context.Background()
	first := signIn(t, s, "")

	for range 3 {
		signIn(t, s, first.DeviceToken)
	}

	for _, limit := range []int{0, -1, user.MaxLoginEvents + 100} {
		events, err := s.LoginEvents(ctx, first.User.ID, limit)
		if err != nil {
			t.Fatalf("LoginEvents(%d) = %v", limit, err)
		}

		if len(events) > user.MaxLoginEvents {
			t.Fatalf("LoginEvents(%d) returned %d entries, want at most %d", limit, len(events), user.MaxLoginEvents)
		}
	}

	// A limit inside the range is honoured rather than clamped away.
	events, err := s.LoginEvents(ctx, first.User.ID, 2)
	if err != nil {
		t.Fatalf("LoginEvents(2) = %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("LoginEvents(2) returned %d entries, want 2", len(events))
	}
}

// TestSignInSanitisesTransportInput covers the two column guards: a user agent
// longer than the 512-char column is truncated rather than rejected, and a peer
// address that will not parse does not take the audit row down with it — the
// inet column is NOT NULL.
func TestSignInSanitisesTransportInput(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	ctx := context.Background()
	sc := user.SignInContext{
		IP:        "not-an-address",
		UserAgent: strings.Repeat("u", 8000),
	}

	result, err := s.SignIn(ctx, testEmail, testPassword, sc)
	if err != nil {
		t.Fatalf("SignIn() = %v", err)
	}

	events, err := s.LoginEvents(ctx, result.User.ID, 1)
	if err != nil {
		t.Fatalf("LoginEvents() = %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}

	if got := len(events[0].UserAgent); got != 512 {
		t.Errorf("user agent length = %d, want it truncated to 512", got)
	}

	if events[0].IP != "0.0.0.0" {
		t.Errorf("ip = %q, want the unknown-address placeholder", events[0].IP)
	}

	devices, err := s.Devices(ctx, result.User.ID)
	if err != nil {
		t.Fatalf("Devices() = %v", err)
	}

	if got := len(devices[0].UserAgent); got != 512 {
		t.Errorf("device user agent length = %d, want it truncated to 512", got)
	}
}
