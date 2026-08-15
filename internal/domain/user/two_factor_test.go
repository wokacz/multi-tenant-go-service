package user_test

import (
	"context"
	"errors"
	"testing"

	"github.com/wokacz/go-example/internal/domain/user"
)

// enableTwoFactor turns the second factor on for an account that has already
// signed in once, and returns a device token that is deliberately *not*
// trusted, so the tests below start from the state that raises a challenge.
func enableTwoFactor(t *testing.T, s *user.Service) (*user.SignInResult, string) {
	t.Helper()

	ctx := context.Background()
	trusted := signIn(t, s, "")

	if err := s.SetTwoFactor(ctx, trusted.User.ID, testPassword, true, laptop(trusted.DeviceToken)); err != nil {
		t.Fatalf("SetTwoFactor() = %v", err)
	}

	// A second, unknown client.
	other := signIn(t, s, "")

	return trusted, other.DeviceToken
}

func TestEnablingTwoFactorTrustsTheCallingDevice(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	trusted, _ := enableTwoFactor(t, s)

	again := signIn(t, s, trusted.DeviceToken)
	if again.Challenged {
		t.Fatal("the device that switched two-factor on should stay trusted")
	}
}

func TestTwoFactorChallengesAnUntrustedDevice(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	_, untrusted := enableTwoFactor(t, s)
	ctx := context.Background()

	result, err := s.SignIn(ctx, testEmail, testPassword, laptop(untrusted))
	if err != nil {
		t.Fatalf("SignIn() = %v", err)
	}

	if !result.Challenged {
		t.Fatal("untrusted device was let in without a code")
	}

	if len(result.Code) != user.TwoFactorCodeLength {
		t.Fatalf("code length = %d, want %d", len(result.Code), user.TwoFactorCodeLength)
	}

	u, device, err := s.VerifyTwoFactor(ctx, testEmail, result.Code, laptop(untrusted))
	if err != nil {
		t.Fatalf("VerifyTwoFactor() = %v", err)
	}

	if !device.IsTrusted() {
		t.Error("verifying should have trusted the device")
	}

	if u.ID != result.User.ID {
		t.Errorf("user = %s, want %s", u.ID, result.User.ID)
	}

	// And the code is spent.
	if _, _, err := s.VerifyTwoFactor(ctx, testEmail, result.Code, laptop(untrusted)); !errors.Is(err, user.ErrInvalidTwoFactorCode) {
		t.Fatalf("replayed code = %v, want ErrInvalidTwoFactorCode", err)
	}

	// The device is trusted now, so the next sign-in skips the challenge.
	if again := signIn(t, s, untrusted); again.Challenged {
		t.Fatal("a verified device should not be challenged again")
	}
}

// TestTwoFactorCodeIsBoundToItsDevice is the reason the challenge stores a
// device id. A code read out of the mailbox must not be spendable from a
// different machine, or the second factor proves only mailbox access.
func TestTwoFactorCodeIsBoundToItsDevice(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	_, untrusted := enableTwoFactor(t, s)
	ctx := context.Background()

	result, err := s.SignIn(ctx, testEmail, testPassword, laptop(untrusted))
	if err != nil {
		t.Fatalf("SignIn() = %v", err)
	}

	// A third client, which never asked for this code.
	elsewhere, err := s.SignIn(ctx, testEmail, testPassword, laptop(""))
	if err != nil {
		t.Fatalf("SignIn(elsewhere) = %v", err)
	}

	_, _, err = s.VerifyTwoFactor(ctx, testEmail, result.Code, laptop(elsewhere.DeviceToken))
	if !errors.Is(err, user.ErrInvalidTwoFactorCode) {
		t.Fatalf("VerifyTwoFactor(other device) = %v, want ErrInvalidTwoFactorCode", err)
	}
}

func TestVerifyTwoFactorHidesEveryFailureMode(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	_, untrusted := enableTwoFactor(t, s)
	ctx := context.Background()

	if _, err := s.SignIn(ctx, testEmail, testPassword, laptop(untrusted)); err != nil {
		t.Fatalf("SignIn() = %v", err)
	}

	cases := map[string]struct {
		email, code, deviceToken string
	}{
		"unknown address":     {"nobody@example.com", "123456", untrusted},
		"no device token":     {testEmail, "123456", ""},
		"unknown device":      {testEmail, "123456", "not-a-real-device-token"},
		"wrong code":          {testEmail, "000000", untrusted},
		"wrong code, no user": {"nobody@example.com", "000000", ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := s.VerifyTwoFactor(ctx, tc.email, tc.code, laptop(tc.deviceToken))
			if !errors.Is(err, user.ErrInvalidTwoFactorCode) {
				t.Fatalf("VerifyTwoFactor() = %v, want ErrInvalidTwoFactorCode", err)
			}
		})
	}
}

func TestTwoFactorCapsAttempts(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	_, untrusted := enableTwoFactor(t, s)
	ctx := context.Background()

	result, err := s.SignIn(ctx, testEmail, testPassword, laptop(untrusted))
	if err != nil {
		t.Fatalf("SignIn() = %v", err)
	}

	wrong := "000000"
	if wrong == result.Code {
		wrong = "111111"
	}

	for i := 1; i <= 5; i++ {
		if _, _, err := s.VerifyTwoFactor(ctx, testEmail, wrong, laptop(untrusted)); !errors.Is(err, user.ErrInvalidTwoFactorCode) {
			t.Fatalf("guess %d = %v, want ErrInvalidTwoFactorCode", i, err)
		}
	}

	if _, _, err := s.VerifyTwoFactor(ctx, testEmail, result.Code, laptop(untrusted)); !errors.Is(err, user.ErrInvalidTwoFactorCode) {
		t.Fatalf("correct code after the cap = %v, want ErrInvalidTwoFactorCode", err)
	}
}

func TestSetTwoFactorRequiresThePassword(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	ctx := context.Background()
	first := signIn(t, s, "")

	err := s.SetTwoFactor(ctx, first.User.ID, "wrong-password", true, laptop(first.DeviceToken))
	if !errors.Is(err, user.ErrInvalidCredentials) {
		t.Fatalf("SetTwoFactor(wrong password) = %v, want ErrInvalidCredentials", err)
	}

	u, err := s.ByID(ctx, first.User.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}

	if u.TwoFactorEnabled {
		t.Fatal("two-factor was switched on without the password")
	}
}

// TestPasswordResetCodeIsNotATwoFactorCode covers the domain separation in the
// HMAC. Both flows email six digits under the same pepper; without the purpose
// prefix a reset code would hash to the same value as a sign-in code and could
// be spent in the wrong place.
func TestPasswordResetCodeIsNotATwoFactorCode(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	_, untrusted := enableTwoFactor(t, s)
	ctx := context.Background()

	if _, err := s.SignIn(ctx, testEmail, testPassword, laptop(untrusted)); err != nil {
		t.Fatalf("SignIn() = %v", err)
	}

	resetCode, err := s.BeginPasswordReset(ctx, testEmail)
	if err != nil {
		t.Fatalf("BeginPasswordReset() = %v", err)
	}

	_, _, err = s.VerifyTwoFactor(ctx, testEmail, resetCode, laptop(untrusted))
	if !errors.Is(err, user.ErrInvalidTwoFactorCode) {
		t.Fatalf("reset code accepted as a sign-in code: %v", err)
	}
}

// TestVerifyRejectsARevokedDevice covers the one branch in VerifyTwoFactor
// that does not collapse into ErrInvalidTwoFactorCode. Revoking a device with
// a challenge outstanding has to stop the code being spent from it.
func TestVerifyRejectsARevokedDevice(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	_, untrusted := enableTwoFactor(t, s)
	ctx := context.Background()

	result, err := s.SignIn(ctx, testEmail, testPassword, laptop(untrusted))
	if err != nil {
		t.Fatalf("SignIn() = %v", err)
	}

	if err := s.RevokeDevice(ctx, result.User.ID, result.Device.ID); err != nil {
		t.Fatalf("RevokeDevice() = %v", err)
	}

	if _, _, err := s.VerifyTwoFactor(ctx, testEmail, result.Code, laptop(untrusted)); !errors.Is(err, user.ErrDeviceRevoked) {
		t.Fatalf("VerifyTwoFactor(revoked) = %v, want ErrDeviceRevoked", err)
	}
}

// TestEnablingTwoFactorWithoutAKnownDevice covers the "nothing to trust" arm.
// A caller with no device token, or one from a database that has since been
// reset, must still be able to switch the setting on — it simply gets no
// trusted device out of it.
func TestEnablingTwoFactorWithoutAKnownDevice(t *testing.T) {
	s, _ := testService(t)
	register(t, s)

	ctx := context.Background()
	first := signIn(t, s, "")

	for _, deviceToken := range []string{"", "a-token-no-device-has"} {
		if err := s.SetTwoFactor(ctx, first.User.ID, testPassword, true, laptop(deviceToken)); err != nil {
			t.Fatalf("SetTwoFactor(deviceToken=%q) = %v", deviceToken, err)
		}
	}

	u, err := s.ByID(ctx, first.User.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}

	if !u.TwoFactorEnabled {
		t.Fatal("two-factor was not switched on")
	}

	// Nothing was trusted, so the original device is now challenged too.
	result, err := s.SignIn(ctx, testEmail, testPassword, laptop(first.DeviceToken))
	if err != nil {
		t.Fatalf("SignIn() = %v", err)
	}

	if !result.Challenged {
		t.Fatal("a device that was never trusted was let straight in")
	}
}
