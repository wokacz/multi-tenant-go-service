package user_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/wokacz/go-example/internal/domain/user"
	"github.com/wokacz/go-example/internal/store/repositories/memory"
)

var testPepper = []byte("0123456789abcdef0123456789abcdef")

func testService(t *testing.T) (*user.Service, *memory.Users) {
	t.Helper()

	repo := memory.NewUsers()

	return user.NewService(repo, testPepper, user.WithBcryptCost(bcrypt.MinCost)), repo
}

func TestCreateTrimsAndRejectsBlankName(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "   ", "a@example.com", "twelve-chars", "twelve-chars", ""); !errors.Is(err, user.ErrNameEmpty) {
		t.Fatalf("Create(blank name) = %v, want ErrNameEmpty", err)
	}

	u, err := s.Create(ctx, "  Ada  ", "a@example.com", "twelve-chars", "twelve-chars", "")
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if u.Name != "Ada" {
		t.Errorf("Name = %q, want Ada", u.Name)
	}
}

func TestCreateRejectsPasswordMismatch(t *testing.T) {
	s, _ := testService(t)

	if _, err := s.Create(context.Background(), "Ada", "a@example.com", "twelve-chars", "twelve-charZ", ""); !errors.Is(err, user.ErrPasswordMismatch) {
		t.Fatalf("Create(mismatch) = %v, want ErrPasswordMismatch", err)
	}
}

func TestCreateRejectsLongName(t *testing.T) {
	s, _ := testService(t)
	name := strings.Repeat("n", user.MaxNameLength+1)

	if _, err := s.Create(context.Background(), name, "a@example.com", "twelve-chars", "twelve-chars", ""); !errors.Is(err, user.ErrNameTooLong) {
		t.Fatalf("Create(long name) = %v, want ErrNameTooLong", err)
	}
}

func TestAuthenticateWrongPasswordAndUnknownEmail(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "Ada", "ada@example.com", "twelve-chars", "twelve-chars", ""); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if _, err := s.Authenticate(ctx, "ada@example.com", "wrong-password"); !errors.Is(err, user.ErrInvalidCredentials) {
		t.Fatalf("wrong password = %v, want ErrInvalidCredentials", err)
	}

	if _, err := s.Authenticate(ctx, "missing@example.com", "twelve-chars"); !errors.Is(err, user.ErrInvalidCredentials) {
		t.Fatalf("unknown email = %v, want ErrInvalidCredentials", err)
	}
}

// TestAuthenticateReportsCancellationNotBadPassword pins the distinction the
// ctx-aware hashing semaphore introduced. bcrypt returns an error both when the
// password is wrong and when the caller gave up waiting for a slot; reporting
// the second as the first would tell a client its password failed when it
// merely disconnected, and on the sign-in path would file a bad_password event
// that never happened.
func TestAuthenticateReportsCancellationNotBadPassword(t *testing.T) {
	s, _ := testService(t)

	if _, err := s.Create(context.Background(), "Ada", "ada@example.com", "twelve-chars", "twelve-chars", ""); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.Authenticate(ctx, "ada@example.com", "twelve-chars")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Authenticate(cancelled) = %v, want context.Canceled", err)
	}
}

func TestAuthenticateSuccessNormalisesEmail(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()

	created, err := s.Create(ctx, "Ada", "Ada@Example.com", "twelve-chars", "twelve-chars", "")
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	got, err := s.Authenticate(ctx, "ADA@example.com", "twelve-chars")
	if err != nil {
		t.Fatalf("Authenticate() = %v", err)
	}

	if got.ID != created.ID {
		t.Errorf("ID = %s, want %s", got.ID, created.ID)
	}
}

func TestPasswordResetUnknownEmailIsSilent(t *testing.T) {
	s, _ := testService(t)

	code, err := s.BeginPasswordReset(context.Background(), "missing@example.com")
	if err != nil {
		t.Fatalf("BeginPasswordReset() = %v", err)
	}

	if code != "" {
		t.Fatalf("code = %q, want empty for an unknown address", code)
	}
}

func TestPasswordResetRoundTrip(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "Ada", "ada@example.com", "twelve-chars", "twelve-chars", ""); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	code, err := s.BeginPasswordReset(ctx, "ada@example.com")
	if err != nil {
		t.Fatalf("BeginPasswordReset() = %v", err)
	}

	if len(code) != user.ResetCodeLength {
		t.Fatalf("code length = %d, want %d", len(code), user.ResetCodeLength)
	}

	if err := s.CompletePasswordReset(ctx, "ada@example.com", code, "another-passw", "another-passw"); err != nil {
		t.Fatalf("CompletePasswordReset() = %v", err)
	}

	if _, err := s.Authenticate(ctx, "ada@example.com", "twelve-chars"); !errors.Is(err, user.ErrInvalidCredentials) {
		t.Fatalf("old password still works: %v", err)
	}

	if _, err := s.Authenticate(ctx, "ada@example.com", "another-passw"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
}

func TestPasswordResetRejectsWrongCode(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "Ada", "ada@example.com", "twelve-chars", "twelve-chars", ""); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if _, err := s.BeginPasswordReset(ctx, "ada@example.com"); err != nil {
		t.Fatalf("BeginPasswordReset() = %v", err)
	}

	if err := s.CompletePasswordReset(ctx, "ada@example.com", "000000", "another-passw", "another-passw"); !errors.Is(err, user.ErrInvalidResetCode) {
		t.Fatalf("CompletePasswordReset(wrong code) = %v, want ErrInvalidResetCode", err)
	}
}

// TestPasswordResetCapsAttempts is the regression test for the counter that
// used to be read, incremented and written back. Each wrong guess has to move
// it, and the fifth has to spend the code — after which even the right code is
// refused.
func TestPasswordResetCapsAttempts(t *testing.T) {
	s, repo := testService(t)
	ctx := context.Background()

	created, err := s.Create(ctx, "Ada", "ada@example.com", "twelve-chars", "twelve-chars", "")
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	code, err := s.BeginPasswordReset(ctx, "ada@example.com")
	if err != nil {
		t.Fatalf("BeginPasswordReset() = %v", err)
	}

	wrong := "000000"
	if wrong == code {
		wrong = "111111"
	}

	for i := 1; i <= 5; i++ {
		if err := s.CompletePasswordReset(ctx, "ada@example.com", wrong, "another-passw", "another-passw"); !errors.Is(err, user.ErrInvalidResetCode) {
			t.Fatalf("guess %d = %v, want ErrInvalidResetCode", i, err)
		}

		attempts, live := repo.Attempts(created.ID)
		if attempts != i {
			t.Fatalf("after guess %d attempts = %d, want %d", i, attempts, i)
		}

		if wantLive := i < 5; live != wantLive {
			t.Fatalf("after guess %d live = %v, want %v", i, live, wantLive)
		}
	}

	if err := s.CompletePasswordReset(ctx, "ada@example.com", code, "another-passw", "another-passw"); !errors.Is(err, user.ErrInvalidResetCode) {
		t.Fatalf("correct code after the cap = %v, want ErrInvalidResetCode", err)
	}
}

func TestPasswordResetRequiresConfirmation(t *testing.T) {
	s, _ := testService(t)

	if err := s.CompletePasswordReset(context.Background(), "ada@example.com", "123456", "another-passw", "nope-nope-no"); !errors.Is(err, user.ErrPasswordMismatch) {
		t.Fatalf("CompletePasswordReset(mismatch) = %v, want ErrPasswordMismatch", err)
	}
}

func TestPasswordResetBumpsSessionEpoch(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()

	created, err := s.Create(ctx, "Ada", "ada@example.com", "twelve-chars", "twelve-chars", "")
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if created.SessionEpoch != 0 {
		t.Fatalf("SessionEpoch = %d, want 0", created.SessionEpoch)
	}

	code, err := s.BeginPasswordReset(ctx, "ada@example.com")
	if err != nil {
		t.Fatalf("BeginPasswordReset() = %v", err)
	}

	if err := s.CompletePasswordReset(ctx, "ada@example.com", code, "another-passw", "another-passw"); err != nil {
		t.Fatalf("CompletePasswordReset() = %v", err)
	}

	got, err := s.ByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}

	if got.SessionEpoch != 1 {
		t.Fatalf("SessionEpoch after reset = %d, want 1", got.SessionEpoch)
	}
}
