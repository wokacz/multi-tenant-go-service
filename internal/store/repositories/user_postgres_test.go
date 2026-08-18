package repositories_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/config"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories"
)

// These tests need Postgres from compose, so they are skipped by default and
// the rest of the suite stays database-free — see AGENTS.md.
//
// They earn their keep by covering what the in-memory repository cannot: the
// SQL itself. The conditional UPDATE that caps attempts, the explicit ::inet
// cast, SELECT ... FOR UPDATE and NULLS LAST are all statements the fake
// reimplements in Go, so a mistake in any of them would pass every other test
// in the project and fail on the first real request.
//
// Run them with:
//
//	POSTGRES_TEST=1 go test ./internal/store/repositories -v
func testDB(t *testing.T) *store.DB {
	t.Helper()

	if os.Getenv("POSTGRES_TEST") == "" {
		t.Skip("set POSTGRES_TEST=1 and run `task up -- postgres && task migrate` to exercise the real store")
	}

	cfg := &config.Config{
		PostgresHost: envOr("POSTGRES_HOST", "localhost"),
		// Read from the environment like everything else here. It was a literal,
		// which meant these tests could only ever run against whatever happened to
		// own port 5432 — on a machine with another project's database there, they
		// could not run at all, and pointing them at it would have run migrations
		// and truncations over somebody else's data.
		PostgresPort:         envOrInt("POSTGRES_PORT", 5432),
		PostgresUser:         envOr("POSTGRES_USER", "postgres"),
		PostgresPassword:     envOr("POSTGRES_PASSWORD", "postgres"),
		PostgresDatabaseName: envOr("POSTGRES_DATABASE_NAME", "postgres"),
		PostgresSSLMode:      envOr("POSTGRES_SSL_MODE", "disable"),
		DBMaxOpenConns:       4,
		DBMaxIdleConns:       4,
		DBConnectTimeout:     5 * time.Second,
		DBSlowQueryThreshold: time.Second,
	}

	db, err := store.OpenPostgres(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("OpenPostgres() = %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	return db
}

func envOrInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}

	return value
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}

// newUser inserts an account with a unique address so parallel runs and
// repeated runs against the same database do not collide.
func newUser(t *testing.T, repo *repositories.User) *models.User {
	t.Helper()

	u := &models.User{
		Name:         "Ada",
		Email:        "ada+" + uuid.Must(uuid.NewV7()).String() + "@example.com",
		PasswordHash: "not-a-real-hash",
	}

	if err := repo.Create(context.Background(), u); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	return u
}

// TestFailPasswordResetIsAtomic is the regression test for the counter that
// used to be read, incremented and written back in Go.
func TestFailPasswordResetIsAtomic(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()
	u := newUser(t, repo)

	reset := &models.PasswordReset{
		UserID:    u.ID,
		CodeHash:  "deadbeef",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := repo.ReplacePasswordReset(ctx, reset); err != nil {
		t.Fatalf("ReplacePasswordReset() = %v", err)
	}

	const maxAttempts = 5

	for i := 1; i <= maxAttempts; i++ {
		if err := repo.FailPasswordReset(ctx, reset.ID, maxAttempts, time.Now().UTC()); err != nil {
			t.Fatalf("FailPasswordReset() = %v", err)
		}

		active, err := repo.ActivePasswordReset(ctx, u.ID, time.Now().UTC())

		if i < maxAttempts {
			if err != nil {
				t.Fatalf("after %d attempts the code should still be live: %v", i, err)
			}

			if active.Attempts != i {
				t.Fatalf("attempts = %d, want %d", active.Attempts, i)
			}

			continue
		}

		// The attempt that reaches the cap spends the code in the same
		// statement that increments the counter.
		if !errors.Is(err, user.ErrNotFound) {
			t.Fatalf("after the cap ActivePasswordReset() = %v, want ErrNotFound", err)
		}
	}

	// Failing a spent code is a no-op rather than an error, and cannot revive it.
	if err := repo.FailPasswordReset(ctx, reset.ID, maxAttempts, time.Now().UTC()); err != nil {
		t.Fatalf("FailPasswordReset(spent) = %v", err)
	}

	if _, err := repo.ActivePasswordReset(ctx, u.ID, time.Now().UTC()); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("spent code came back: %v", err)
	}
}

// TestFailPasswordResetUnderConcurrency is the property the old code did not
// have. Every guess has to land, even when they overlap.
func TestFailPasswordResetUnderConcurrency(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()
	u := newUser(t, repo)

	reset := &models.PasswordReset{
		UserID:    u.ID,
		CodeHash:  "deadbeef",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := repo.ReplacePasswordReset(ctx, reset); err != nil {
		t.Fatalf("ReplacePasswordReset() = %v", err)
	}

	const (
		guesses     = 3
		maxAttempts = 100 // high enough that the cap does not interfere
	)

	errCh := make(chan error, guesses)
	start := make(chan struct{})

	for range guesses {
		go func() {
			<-start
			errCh <- repo.FailPasswordReset(ctx, reset.ID, maxAttempts, time.Now().UTC())
		}()
	}

	close(start)

	for range guesses {
		if err := <-errCh; err != nil {
			t.Fatalf("FailPasswordReset() = %v", err)
		}
	}

	active, err := repo.ActivePasswordReset(ctx, u.ID, time.Now().UTC())
	if err != nil {
		t.Fatalf("ActivePasswordReset() = %v", err)
	}

	if active.Attempts != guesses {
		t.Fatalf("attempts = %d, want %d — overlapping guesses were lost", active.Attempts, guesses)
	}
}

// TestTouchDeviceWritesInet covers the explicit ::inet cast. Without it the
// driver sends an untyped parameter for an inet column, which is the kind of
// thing that works in a unit test and fails on the first real request.
func TestTouchDeviceWritesInet(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()
	u := newUser(t, repo)

	device := &models.Device{UserID: u.ID, Fingerprint: uuid.Must(uuid.NewV7()).String()}
	if err := repo.CreateDevice(ctx, device); err != nil {
		t.Fatalf("CreateDevice() = %v", err)
	}

	for _, ip := range []string{"192.0.2.10", "2001:db8::1"} {
		if err := repo.TouchDevice(ctx, device.ID, time.Now().UTC(), ip, "test-agent"); err != nil {
			t.Fatalf("TouchDevice(%s) = %v", ip, err)
		}

		got, err := repo.ActiveDevice(ctx, u.ID, device.ID)
		if err != nil {
			t.Fatalf("ActiveDevice() = %v", err)
		}

		if got.LastIP == nil || *got.LastIP != ip {
			t.Fatalf("last ip = %v, want %s", got.LastIP, ip)
		}

		if got.LastSeenAt == nil {
			t.Fatal("last seen was not recorded")
		}
	}
}

// TestRevokeAndTrustApplyTheModelRules checks that the FOR UPDATE read plus
// models.Device is really what decides, including that revoking clears trust
// and that a revoked device cannot be trusted again.
func TestRevokeAndTrustApplyTheModelRules(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()
	u := newUser(t, repo)

	device := &models.Device{UserID: u.ID, Fingerprint: uuid.Must(uuid.NewV7()).String()}
	if err := repo.CreateDevice(ctx, device); err != nil {
		t.Fatalf("CreateDevice() = %v", err)
	}

	if err := repo.TrustDevice(ctx, device.ID); err != nil {
		t.Fatalf("TrustDevice() = %v", err)
	}

	trusted, err := repo.ActiveDevice(ctx, u.ID, device.ID)
	if err != nil {
		t.Fatalf("ActiveDevice() = %v", err)
	}

	if !trusted.IsTrusted() {
		t.Fatal("device was not trusted")
	}

	if err := repo.RevokeDevice(ctx, u.ID, device.ID); err != nil {
		t.Fatalf("RevokeDevice() = %v", err)
	}

	// Revoking twice succeeds.
	if err := repo.RevokeDevice(ctx, u.ID, device.ID); err != nil {
		t.Fatalf("RevokeDevice(twice) = %v", err)
	}

	if _, err := repo.ActiveDevice(ctx, u.ID, device.ID); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("ActiveDevice(revoked) = %v, want ErrNotFound", err)
	}

	// And a revoked device cannot be trusted back into service.
	if err := repo.TrustDevice(ctx, device.ID); !errors.Is(err, user.ErrDeviceRevoked) {
		t.Fatalf("TrustDevice(revoked) = %v, want ErrDeviceRevoked", err)
	}

	// Another account's device is invisible rather than forbidden.
	other := newUser(t, repo)
	if err := repo.RevokeDevice(ctx, other.ID, device.ID); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("RevokeDevice(other account) = %v, want ErrNotFound", err)
	}
}

// TestDevicesOrdersNullsLast covers the ORDER BY. Postgres sorts NULL first on
// DESC by default, which would put a device that has never been seen above
// every device that has.
func TestDevicesOrdersNullsLast(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()
	u := newUser(t, repo)

	never := &models.Device{UserID: u.ID, Fingerprint: uuid.Must(uuid.NewV7()).String()}
	seen := &models.Device{UserID: u.ID, Fingerprint: uuid.Must(uuid.NewV7()).String()}

	for _, d := range []*models.Device{never, seen} {
		if err := repo.CreateDevice(ctx, d); err != nil {
			t.Fatalf("CreateDevice() = %v", err)
		}
	}

	if err := repo.TouchDevice(ctx, seen.ID, time.Now().UTC(), "192.0.2.10", "agent"); err != nil {
		t.Fatalf("TouchDevice() = %v", err)
	}

	devices, err := repo.Devices(ctx, u.ID)
	if err != nil {
		t.Fatalf("Devices() = %v", err)
	}

	if len(devices) != 2 {
		t.Fatalf("devices = %d, want 2", len(devices))
	}

	if devices[0].ID != seen.ID {
		t.Fatalf("first device = %s, want the one that has been seen %s", devices[0].ID, seen.ID)
	}
}

// TestConsumeTwoFactorChallengeIsSingleUse covers the RowsAffected guard: two
// requests racing on one code must not both be let in.
func TestConsumeTwoFactorChallengeIsSingleUse(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()
	u := newUser(t, repo)

	device := &models.Device{UserID: u.ID, Fingerprint: uuid.Must(uuid.NewV7()).String()}
	if err := repo.CreateDevice(ctx, device); err != nil {
		t.Fatalf("CreateDevice() = %v", err)
	}

	challenge := &models.TwoFactorChallenge{
		UserID:    u.ID,
		DeviceID:  device.ID,
		CodeHash:  "deadbeef",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := repo.ReplaceTwoFactorChallenge(ctx, challenge); err != nil {
		t.Fatalf("ReplaceTwoFactorChallenge() = %v", err)
	}

	if err := repo.ConsumeTwoFactorChallenge(ctx, challenge.ID, device.ID, time.Now().UTC()); err != nil {
		t.Fatalf("ConsumeTwoFactorChallenge() = %v", err)
	}

	err := repo.ConsumeTwoFactorChallenge(ctx, challenge.ID, device.ID, time.Now().UTC())
	if !errors.Is(err, user.ErrInvalidTwoFactorCode) {
		t.Fatalf("second consume = %v, want ErrInvalidTwoFactorCode", err)
	}

	trusted, err := repo.ActiveDevice(ctx, u.ID, device.ID)
	if err != nil {
		t.Fatalf("ActiveDevice() = %v", err)
	}

	if !trusted.IsTrusted() {
		t.Fatal("consuming the challenge did not trust the device")
	}
}

// TestLoginEventsRoundTrip checks the inet column and the ordering on the
// history query.
func TestLoginEventsRoundTrip(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()
	u := newUser(t, repo)

	device := &models.Device{UserID: u.ID, Fingerprint: uuid.Must(uuid.NewV7()).String()}
	if err := repo.CreateDevice(ctx, device); err != nil {
		t.Fatalf("CreateDevice() = %v", err)
	}

	outcomes := []models.LoginOutcome{models.OutcomeSuccess, models.OutcomeBadPassword}
	for _, outcome := range outcomes {
		event := &models.LoginEvent{
			UserID:    u.ID,
			DeviceID:  &device.ID,
			IP:        "192.0.2.10",
			UserAgent: "agent",
			Outcome:   outcome,
		}

		if err := repo.RecordLoginEvent(ctx, event); err != nil {
			t.Fatalf("RecordLoginEvent(%s) = %v", outcome, err)
		}

		// created_at has second-level ties otherwise, and the ordering
		// assertion below would be a coin flip.
		time.Sleep(2 * time.Millisecond)
	}

	events, err := repo.LoginEvents(ctx, u.ID, 10)
	if err != nil {
		t.Fatalf("LoginEvents() = %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}

	if events[0].Outcome != models.OutcomeBadPassword {
		t.Errorf("newest outcome = %q, want bad_password", events[0].Outcome)
	}

	// The check constraint has to reject anything the enum does not name.
	bad := &models.LoginEvent{UserID: u.ID, IP: "192.0.2.10", Outcome: models.LoginOutcome("nonsense")}
	if err := repo.RecordLoginEvent(ctx, bad); err == nil {
		t.Fatal("RecordLoginEvent accepted an unknown outcome")
	}
}

// TestCreateTranslatesDuplicateEmail covers the branch that depends on GORM's
// TranslateError flag being set in store.OpenPostgres. Without it the unique
// violation arrives as a raw pgx error and registration answers 500 instead of
// the deliberate 204, and no test that stubs the repository would notice.
func TestCreateTranslatesDuplicateEmail(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()
	first := newUser(t, repo)

	again := &models.User{Name: "Ada", Email: first.Email, PasswordHash: "another-hash"}
	if err := repo.Create(ctx, again); !errors.Is(err, user.ErrEmailTaken) {
		t.Fatalf("Create(duplicate) = %v, want ErrEmailTaken", err)
	}
}

func TestUserLookups(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()
	created := newUser(t, repo)

	byID, err := repo.ByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}

	byEmail, err := repo.ByEmail(ctx, created.Email)
	if err != nil {
		t.Fatalf("ByEmail() = %v", err)
	}

	if byID.ID != created.ID || byEmail.ID != created.ID {
		t.Fatalf("lookups disagree: byID=%s byEmail=%s want %s", byID.ID, byEmail.ID, created.ID)
	}

	if byID.TwoFactorEnabled {
		t.Error("a new account should not have two-factor on")
	}

	if _, err := repo.ByID(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, user.ErrNotFound) {
		t.Errorf("ByID(missing) = %v, want ErrNotFound", err)
	}

	if _, err := repo.ByEmail(ctx, "nobody@example.com"); !errors.Is(err, user.ErrNotFound) {
		t.Errorf("ByEmail(missing) = %v, want ErrNotFound", err)
	}
}

// TestConsumePasswordResetIsTransactional checks that the new hash, the epoch
// bump and the spent code all land. The epoch is what invalidates tokens issued
// under the old password, so a partial write here would leave those tokens
// working against a password their holder no longer knows.
func TestConsumePasswordResetIsTransactional(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()
	u := newUser(t, repo)

	reset := &models.PasswordReset{
		UserID:    u.ID,
		CodeHash:  "deadbeef",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := repo.ReplacePasswordReset(ctx, reset); err != nil {
		t.Fatalf("ReplacePasswordReset() = %v", err)
	}

	spent := time.Now().UTC()
	reset.ConsumedAt = &spent

	if err := repo.ConsumePasswordReset(ctx, reset, "the-new-hash"); err != nil {
		t.Fatalf("ConsumePasswordReset() = %v", err)
	}

	got, err := repo.ByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}

	if got.PasswordHash != "the-new-hash" {
		t.Errorf("password hash = %q, want the new one", got.PasswordHash)
	}

	if got.SessionEpoch != u.SessionEpoch+1 {
		t.Errorf("session epoch = %d, want %d", got.SessionEpoch, u.SessionEpoch+1)
	}

	if _, err := repo.ActivePasswordReset(ctx, u.ID, time.Now().UTC()); !errors.Is(err, user.ErrNotFound) {
		t.Errorf("the code survived being consumed: %v", err)
	}
}

// TestDeviceByFingerprintIsScopedToTheUser covers the user_id predicate. Two
// accounts on the same browser legitimately share a fingerprint, so the lookup
// must not be able to return the other account's device.
func TestDeviceByFingerprintIsScopedToTheUser(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()

	ada, bob := newUser(t, repo), newUser(t, repo)
	shared := uuid.Must(uuid.NewV7()).String()

	adaDevice := &models.Device{UserID: ada.ID, Fingerprint: shared}
	bobDevice := &models.Device{UserID: bob.ID, Fingerprint: shared}

	for _, d := range []*models.Device{adaDevice, bobDevice} {
		if err := repo.CreateDevice(ctx, d); err != nil {
			t.Fatalf("CreateDevice() = %v", err)
		}
	}

	got, err := repo.DeviceByFingerprint(ctx, ada.ID, shared)
	if err != nil {
		t.Fatalf("DeviceByFingerprint() = %v", err)
	}

	if got.ID != adaDevice.ID {
		t.Fatalf("device = %s, want ada's %s", got.ID, adaDevice.ID)
	}

	if _, err := repo.DeviceByFingerprint(ctx, ada.ID, "no-such-fingerprint"); !errors.Is(err, user.ErrNotFound) {
		t.Errorf("DeviceByFingerprint(unknown) = %v, want ErrNotFound", err)
	}
}

// TestFailTwoFactorChallengeIsAtomic is the sign-in code's half of the
// conditional UPDATE. It guards the same property as the password-reset test:
// overlapping guesses must all count, and the cap must spend the code.
func TestFailTwoFactorChallengeIsAtomic(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()
	u := newUser(t, repo)

	device := &models.Device{UserID: u.ID, Fingerprint: uuid.Must(uuid.NewV7()).String()}
	if err := repo.CreateDevice(ctx, device); err != nil {
		t.Fatalf("CreateDevice() = %v", err)
	}

	challenge := &models.TwoFactorChallenge{
		UserID:    u.ID,
		DeviceID:  device.ID,
		CodeHash:  "deadbeef",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := repo.ReplaceTwoFactorChallenge(ctx, challenge); err != nil {
		t.Fatalf("ReplaceTwoFactorChallenge() = %v", err)
	}

	const maxAttempts = 3

	// Overlapping guesses, to prove none is lost.
	errCh := make(chan error, maxAttempts-1)
	start := make(chan struct{})

	for range maxAttempts - 1 {
		go func() {
			<-start
			errCh <- repo.FailTwoFactorChallenge(ctx, challenge.ID, maxAttempts, time.Now().UTC())
		}()
	}

	close(start)

	for range maxAttempts - 1 {
		if err := <-errCh; err != nil {
			t.Fatalf("FailTwoFactorChallenge() = %v", err)
		}
	}

	live, err := repo.ActiveTwoFactorChallenge(ctx, u.ID, time.Now().UTC())
	if err != nil {
		t.Fatalf("ActiveTwoFactorChallenge() = %v", err)
	}

	if live.Attempts != maxAttempts-1 {
		t.Fatalf("attempts = %d, want %d — overlapping guesses were lost", live.Attempts, maxAttempts-1)
	}

	// The guess that reaches the cap spends the code.
	if err := repo.FailTwoFactorChallenge(ctx, challenge.ID, maxAttempts, time.Now().UTC()); err != nil {
		t.Fatalf("FailTwoFactorChallenge() = %v", err)
	}

	if _, err := repo.ActiveTwoFactorChallenge(ctx, u.ID, time.Now().UTC()); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("challenge survived the cap: %v", err)
	}

	// And a spent challenge cannot be redeemed.
	err = repo.ConsumeTwoFactorChallenge(ctx, challenge.ID, device.ID, time.Now().UTC())
	if !errors.Is(err, user.ErrInvalidTwoFactorCode) {
		t.Fatalf("ConsumeTwoFactorChallenge(spent) = %v, want ErrInvalidTwoFactorCode", err)
	}
}

// TestActiveTwoFactorChallengeIgnoresExpired covers the expires_at predicate,
// which is the only thing bounding how long a code stays usable.
func TestActiveTwoFactorChallengeIgnoresExpired(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()
	u := newUser(t, repo)

	device := &models.Device{UserID: u.ID, Fingerprint: uuid.Must(uuid.NewV7()).String()}
	if err := repo.CreateDevice(ctx, device); err != nil {
		t.Fatalf("CreateDevice() = %v", err)
	}

	expired := &models.TwoFactorChallenge{
		UserID:    u.ID,
		DeviceID:  device.ID,
		CodeHash:  "deadbeef",
		ExpiresAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := repo.ReplaceTwoFactorChallenge(ctx, expired); err != nil {
		t.Fatalf("ReplaceTwoFactorChallenge() = %v", err)
	}

	if _, err := repo.ActiveTwoFactorChallenge(ctx, u.ID, time.Now().UTC()); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("ActiveTwoFactorChallenge(expired) = %v, want ErrNotFound", err)
	}
}

func TestSetTwoFactorEnabled(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()
	u := newUser(t, repo)

	for _, want := range []bool{true, false} {
		if err := repo.SetTwoFactorEnabled(ctx, u.ID, want); err != nil {
			t.Fatalf("SetTwoFactorEnabled(%v) = %v", want, err)
		}

		got, err := repo.ByID(ctx, u.ID)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}

		if got.TwoFactorEnabled != want {
			t.Fatalf("TwoFactorEnabled = %v, want %v", got.TwoFactorEnabled, want)
		}
	}
}

// TestUpdateProfileWritesBothColumns covers the one statement behind PATCH /v1/me.
//
// It skips hooks, the same as UpdateOrganization, because GORM would run
// User.BeforeSave against the zero-valued struct handed to Model and judge an
// empty address. That makes this the kind of query where a wrong column name or a
// missed WHERE is invisible until somebody's profile is somebody else's.
func TestUpdateProfileWritesBothColumns(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()
	u := newUser(t, repo)
	other := newUser(t, repo)

	if err := repo.UpdateProfile(ctx, u.ID, "Ada Lovelace", "pl"); err != nil {
		t.Fatalf("UpdateProfile() = %v", err)
	}

	got, err := repo.ByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}

	if got.Name != "Ada Lovelace" || got.Locale != "pl" {
		t.Errorf("name/locale = %q/%q, want %q/%q", got.Name, got.Locale, "Ada Lovelace", "pl")
	}

	if got.Email != u.Email {
		t.Errorf("email = %q, want it untouched at %q", got.Email, u.Email)
	}

	if got.SessionEpoch != u.SessionEpoch {
		t.Errorf("session epoch moved to %d; a profile edit is not a credential change",
			got.SessionEpoch)
	}

	// The WHERE, stated as a test rather than trusted.
	untouched, err := repo.ByID(ctx, other.ID)
	if err != nil {
		t.Fatalf("ByID(other) = %v", err)
	}

	if untouched.Name == "Ada Lovelace" {
		t.Error("the other account was renamed too")
	}

	// Clearing the locale has to reach the column. The update is built from a map
	// for exactly this reason: GORM skips zero values when it is handed a struct,
	// so a struct here would silently keep "pl" while the fake cleared it — and the
	// only visible symptom would be a language nobody can switch off.
	if err := repo.UpdateProfile(ctx, u.ID, "Ada Lovelace", ""); err != nil {
		t.Fatalf("UpdateProfile() clearing the locale = %v", err)
	}

	cleared, err := repo.ByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}

	if cleared.Locale != "" {
		t.Errorf("locale = %q, want it cleared", cleared.Locale)
	}

	if err := repo.UpdateProfile(ctx, uuid.Must(uuid.NewV7()), "Nobody", ""); !errors.Is(err, user.ErrNotFound) {
		t.Errorf("UpdateProfile() on an unknown id = %v, want ErrNotFound", err)
	}
}

// TestSetPasswordAndBumpEpochMoveTheEpochByOne pins the increment as an expression.
//
// Read-modify-write would be indistinguishable here with one caller and wrong with
// two: both would read the same value and write the same value, and a token issued
// under the old epoch would survive one of the two changes.
func TestSetPasswordAndBumpEpochMoveTheEpochByOne(t *testing.T) {
	repo := repositories.NewUser(testDB(t))
	ctx := context.Background()
	u := newUser(t, repo)

	if err := repo.SetPassword(ctx, u.ID, "the-new-hash"); err != nil {
		t.Fatalf("SetPassword() = %v", err)
	}

	afterPassword, err := repo.ByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}

	if afterPassword.PasswordHash != "the-new-hash" {
		t.Errorf("password hash = %q, want the new one", afterPassword.PasswordHash)
	}

	if afterPassword.SessionEpoch != u.SessionEpoch+1 {
		t.Errorf("session epoch = %d, want %d", afterPassword.SessionEpoch, u.SessionEpoch+1)
	}

	if err := repo.BumpSessionEpoch(ctx, u.ID); err != nil {
		t.Fatalf("BumpSessionEpoch() = %v", err)
	}

	afterBump, err := repo.ByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}

	if afterBump.SessionEpoch != afterPassword.SessionEpoch+1 {
		t.Errorf("session epoch = %d, want %d",
			afterBump.SessionEpoch, afterPassword.SessionEpoch+1)
	}

	if afterBump.PasswordHash != "the-new-hash" {
		t.Error("signing out everywhere changed the password; it must only move the epoch")
	}

	if err := repo.BumpSessionEpoch(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, user.ErrNotFound) {
		t.Errorf("BumpSessionEpoch() on an unknown id = %v, want ErrNotFound", err)
	}
}
