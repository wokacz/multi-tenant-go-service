package httptest

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/wokacz/multi-tenant-go-service/internal/api"
)

const newAddress = "ada.new@example.com"

func beginEmailChange(t *testing.T, s *api.Server, token, address, password string) *httpResult {
	t.Helper()

	body := fmt.Sprintf(`{"new_email":%q,"password":%q}`, address, password)
	rec := Do(t, s.Handler(), Authed(t, http.MethodPost, "/v1/me/email", body, token, ""))

	return &httpResult{code: rec.Code, body: rec.Body.Bytes()}
}

// TestChangingAnAddressNeedsTheCodeFromIt is the whole point of the two steps.
//
// The address on an account is where a password reset goes, so applying a new one
// on Request alone would turn this into a way to take over an account with nothing
// but a borrowed token. Nothing moves until a code read out of the new mailbox
// comes back.
func TestChangingAnAddressNeedsTheCodeFromIt(t *testing.T) {
	server, mailer := NewTestServer(t)
	RegisterAda(t, server)
	session := SignInAda(t, server, "", http.StatusCreated)

	if res := beginEmailChange(t, server, session.Token, newAddress, TestPassword); res.code != http.StatusNoContent {
		t.Fatalf("begin = %d, want 204; body %s", res.code, res.body)
	}

	// The code went to the new address, not the current one.
	if mailer.EmailChangeTo != newAddress {
		t.Errorf("code was sent to %q, want %q", mailer.EmailChangeTo, newAddress)
	}

	// And the account has not moved yet: the old address still signs in.
	SignInAda(t, server, "", http.StatusCreated)

	body := fmt.Sprintf(`{"code":%q}`, mailer.EmailChangeCode)
	rec := Do(t, server.Handler(),
		Authed(t, http.MethodPost, "/v1/me/email/verify", body, session.Token, ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("confirm = %d, want 204; body %s", rec.Code, rec.Body.Bytes())
	}

	// Now it has. The new address signs in and the old one does not.
	if got := signInStatus(t, server, newAddress, TestPassword); got != http.StatusCreated {
		t.Errorf("sign in with the new address = %d, want 201", got)
	}

	if got := signInStatus(t, server, TestEmail, TestPassword); got != http.StatusUnauthorized {
		t.Errorf("sign in with the old address = %d, want 401", got)
	}

	// The token issued before the change still works: the password did not
	// change, so signing every device out would be a surprise with no benefit.
	rec = Do(t, server.Handler(), Authed(t, http.MethodGet, "/v1/me", "", session.Token, ""))
	if rec.Code != http.StatusOK {
		t.Errorf("the session that made the change = %d, want it still valid", rec.Code)
	}
}

// TestChangingAnAddressNeedsThePassword keeps a leaked token from redirecting
// where the account can be recovered from.
func TestChangingAnAddressNeedsThePassword(t *testing.T) {
	server, mailer := NewTestServer(t)
	RegisterAda(t, server)
	session := SignInAda(t, server, "", http.StatusCreated)

	res := beginEmailChange(t, server, session.Token, newAddress, "not-the-password")
	if res.code != http.StatusUnauthorized {
		t.Fatalf("begin with a wrong password = %d, want 401; body %s", res.code, res.body)
	}

	if mailer.EmailChangeCode != "" {
		t.Error("a code was sent despite the wrong password")
	}
}

// TestAWrongConfirmationCodeIsRefused covers the other end. A wrong code, an
// expired one and no outstanding change at all share one answer, the same way the
// reset code does.
func TestAWrongConfirmationCodeIsRefused(t *testing.T) {
	server, _ := NewTestServer(t)
	RegisterAda(t, server)
	session := SignInAda(t, server, "", http.StatusCreated)

	// No outstanding change.
	rec := Do(t, server.Handler(),
		Authed(t, http.MethodPost, "/v1/me/email/verify", `{"code":"000000"}`, session.Token, ""))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("confirm with no change pending = %d, want 401", rec.Code)
	}

	beginEmailChange(t, server, session.Token, newAddress, TestPassword)

	rec = Do(t, server.Handler(),
		Authed(t, http.MethodPost, "/v1/me/email/verify", `{"code":"000000"}`, session.Token, ""))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("confirm with a wrong code = %d, want 401", rec.Code)
	}

	// Still on the old address.
	if got := signInStatus(t, server, TestEmail, TestPassword); got != http.StatusCreated {
		t.Errorf("sign in with the original address = %d, want it unchanged", got)
	}
}

// TestBeginningAnEmailChangeDoesNotSayWhetherTheAddressIsTaken is the enumeration
// rule this operation has to obey.
//
// An authenticated caller could otherwise walk a list of addresses one Request at
// a time, which is the oracle registration goes to some length to close. The
// answer is given at confirmation instead — by which point the caller has read a
// code out of that mailbox, and somebody who can Do that was going to find out.
func TestBeginningAnEmailChangeDoesNotSayWhetherTheAddressIsTaken(t *testing.T) {
	server, mailer := NewTestServer(t)
	RegisterAda(t, server)

	const taken = "bob@example.com"

	body := `{"name":"Bob","email":"` + taken + `","password":"twelve-chars","password_confirm":"twelve-chars"}`
	if rec := PostJSON(t, server.Handler(), "/v1/users", body); rec.Code != http.StatusNoContent {
		t.Fatalf("register the other account = %d", rec.Code)
	}

	session := SignInAda(t, server, "", http.StatusCreated)

	free := beginEmailChange(t, server, session.Token, newAddress, TestPassword)
	used := beginEmailChange(t, server, session.Token, taken, TestPassword)

	if free.code != used.code {
		t.Errorf("free address = %d, taken address = %d; the two must be indistinguishable",
			free.code, used.code)
	}

	// The refusal comes at confirmation, where reading the code has already proved
	// control of the mailbox.
	confirm := fmt.Sprintf(`{"code":%q}`, mailer.EmailChangeCode)

	rec := Do(t, server.Handler(),
		Authed(t, http.MethodPost, "/v1/me/email/verify", confirm, session.Token, ""))
	if rec.Code != http.StatusConflict {
		t.Errorf("confirm onto a taken address = %d, want 409; body %s", rec.Code, rec.Body.Bytes())
	}
}

// TestTheSameAddressIsRefused stops the operation from being a way to send mail
// with nothing else to show for it.
func TestTheSameAddressIsRefused(t *testing.T) {
	server, mailer := NewTestServer(t)
	RegisterAda(t, server)
	session := SignInAda(t, server, "", http.StatusCreated)

	res := beginEmailChange(t, server, session.Token, TestEmail, TestPassword)
	if res.code != http.StatusUnprocessableEntity {
		t.Errorf("begin with the current address = %d, want 422; body %s", res.code, res.body)
	}

	if mailer.EmailChangeCode != "" {
		t.Error("a code was sent for an address the account already has")
	}
}

// signInStatus is sign-in reduced to its status, for the cases that are about
// whether the address works rather than about the session.
func signInStatus(t *testing.T, s *api.Server, email, password string) int {
	t.Helper()

	body := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)

	return Do(t, s.Handler(), WithDeviceToken(Request(t, http.MethodPost, "/v1/sessions", body))).Code
}
