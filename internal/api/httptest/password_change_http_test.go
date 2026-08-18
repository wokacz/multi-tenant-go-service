package httptest

import (
	"net/http"
	"testing"
)

// TestChangingThePasswordEndsEverySession is the behaviour a client has to be
// ready for, and it is deliberate rather than incidental.
//
// Somebody changing their password either thinks it was known to somebody else, in
// which case every session should end, or they Do not, in which case signing in
// again costs one screen. Keeping this session alive would mean issuing a token
// here, and the endpoint that issues tokens is the one that decides whether a
// second factor is needed.
func TestChangingThePasswordEndsEverySession(t *testing.T) {
	f := NewAuthzFixture(t)

	const next = "a-longer-secret"

	body := `{"current_password":"` + TestPassword + `","password":"` + next +
		`","password_confirm":"` + next + `"}`

	f.call(t, http.MethodPost, "/v1/me/password", body).
		expect(t, http.StatusNoContent)

	// The token that made the change is dead with the rest of them.
	f.call(t, http.MethodGet, "/v1/me", "").expect(t, http.StatusUnauthorized)

	if got := signInStatus(t, f.Server, TestEmail, TestPassword); got != http.StatusUnauthorized {
		t.Errorf("the old password still signs in (%d)", got)
	}

	if got := signInStatus(t, f.Server, TestEmail, next); got != http.StatusCreated {
		t.Errorf("the new password does not sign in (%d)", got)
	}
}

// TestChangingThePasswordNeedsTheCurrentOne is why a bearer token is not enough. A
// token that leaked out of a browser must not be able to make the one change that
// locks the owner out of their own account.
func TestChangingThePasswordNeedsTheCurrentOne(t *testing.T) {
	f := NewAuthzFixture(t)

	const next = "a-longer-secret"

	body := `{"current_password":"not-the-password","password":"` + next +
		`","password_confirm":"` + next + `"}`

	var doc ProblemBody
	f.call(t, http.MethodPost, "/v1/me/password", body).
		expect(t, http.StatusUnauthorized).decode(t, &doc)

	if doc.Code != "invalid_credentials" {
		t.Errorf("code = %q, want invalid_credentials", doc.Code)
	}

	if got := signInStatus(t, f.Server, TestEmail, TestPassword); got != http.StatusCreated {
		t.Errorf("the old password no longer signs in (%d); a refused change must "+
			"change nothing", got)
	}
}

// TestANewPasswordMustBeConfirmed covers the pair of rules the reset path already
// applies, reached from the other direction.
func TestANewPasswordMustBeConfirmed(t *testing.T) {
	f := NewAuthzFixture(t)

	var doc ProblemBody
	f.call(t, http.MethodPost, "/v1/me/password",
		`{"current_password":"`+TestPassword+`","password":"a-longer-secret","password_confirm":"a-longer-secre"}`).
		expect(t, http.StatusUnprocessableEntity).decode(t, &doc)

	if doc.Code != "password_mismatch" {
		t.Errorf("code = %q, want password_mismatch", doc.Code)
	}
}

// TestSigningOutEverywhereKillsTheTokensAndKeepsThePassword is the endpoint that
// did not exist: bumping the epoch was only ever a side effect of changing a
// password or being suspended, and wanting one without the other is the ordinary
// case — a session left open on a machine somebody no longer has.
func TestSigningOutEverywhereKillsTheTokensAndKeepsThePassword(t *testing.T) {
	f := NewAuthzFixture(t)

	// A second session for the same account, to show this is not just about the
	// token that asked.
	other := SignInAda(t, f.Server, "", http.StatusCreated)

	f.call(t, http.MethodDelete, "/v1/me/sessions", "").expect(t, http.StatusNoContent)

	f.call(t, http.MethodGet, "/v1/me", "").expect(t, http.StatusUnauthorized)

	if got := Do(t, f.Server.Handler(),
		Authed(t, http.MethodGet, "/v1/me", "", other.Token, "")).Code; got != http.StatusUnauthorized {
		t.Errorf("the other session survived (%d)", got)
	}

	if got := signInStatus(t, f.Server, TestEmail, TestPassword); got != http.StatusCreated {
		t.Errorf("signing in again = %d, want 201; the password is untouched", got)
	}
}

// TestSigningOutEverywhereLeavesDevicesAlone keeps the two ideas apart. Ending
// sessions is not the same as withdrawing a device's right to hold one, and a
// client that wanted the second has DELETE /v1/me/devices/{id}.
//
// Signing in again uses the device token from before, because a sign-in without one
// mints a new device — which would make the count go up for a reason that has
// nothing to Do with what this is testing.
func TestSigningOutEverywhereLeavesDevicesAlone(t *testing.T) {
	f := NewAuthzFixture(t)

	known := SignInAda(t, f.Server, "", http.StatusCreated)
	f.Token = known.Token

	before := len(f.devices(t))
	if before == 0 {
		t.Fatal("signing in recorded no device; this test cannot tell whether one " +
			"survived")
	}

	f.call(t, http.MethodDelete, "/v1/me/sessions", "").expect(t, http.StatusNoContent)

	// 201 rather than the 401 a revoked device gets: the device is still trusted,
	// only the tokens are gone.
	again := SignInAda(t, f.Server, known.DeviceToken, http.StatusCreated)
	f.Token = again.Token

	if after := len(f.devices(t)); after != before {
		t.Errorf("device count went from %d to %d", before, after)
	}
}

// devices lists the caller's devices.
func (f *AuthzFixture) devices(t *testing.T) []struct {
	ID string `json:"id"`
} {
	t.Helper()

	var body struct {
		Devices []struct {
			ID string `json:"id"`
		} `json:"devices"`
	}
	f.call(t, http.MethodGet, "/v1/me/devices", "").
		expect(t, http.StatusOK).decode(t, &body)

	return body.Devices
}
