package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

type deviceList struct {
	Devices []struct {
		ID      uuid.UUID `json:"id"`
		Trusted bool      `json:"trusted"`
		Revoked bool      `json:"revoked"`
		Current bool      `json:"current"`
		LastIP  string    `json:"last_ip"`
	} `json:"devices"`
}

type eventList struct {
	Events []struct {
		Outcome string `json:"outcome"`
		IP      string `json:"ip"`
	} `json:"events"`
}

func decodeInto[T any](t *testing.T, body []byte) T {
	t.Helper()

	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, body)
	}

	return out
}

// enableTwoFactorOverHTTP walks the real endpoints rather than reaching into
// the service, so the test covers the wiring as well as the rules.
func enableTwoFactorOverHTTP(t *testing.T, s *Server) sessionBody {
	t.Helper()

	registerAda(t, s)
	first := signInAda(t, s, "", http.StatusCreated)

	rec := do(t, s.http.Handler, authed(t, http.MethodPut, "/v1/me/two-factor",
		`{"password":"`+testPassword+`","enabled":true}`, first.Token, first.DeviceToken))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("enable two-factor status = %d body %s", rec.Code, rec.Body.Bytes())
	}

	return first
}

func TestTwoFactorSignInOverHTTP(t *testing.T) {
	s, mailer := newTestServer(t)
	trusted := enableTwoFactorOverHTTP(t, s)

	// The device that switched it on stays trusted and is let straight in.
	if again := signInAda(t, s, trusted.DeviceToken, http.StatusCreated); again.TwoFactorRequired {
		t.Fatal("the enrolling device was challenged")
	}

	// A client with no device token is not.
	challenge := signInAda(t, s, "", http.StatusAccepted)

	if !challenge.TwoFactorRequired {
		t.Fatal("two_factor_required is false on a 202")
	}

	if challenge.Token != "" {
		t.Fatal("a token was issued alongside the challenge")
	}

	if challenge.DeviceToken == "" {
		t.Fatal("the challenge did not hand out a device token")
	}

	if mailer.twoFactorCode == "" || mailer.twoFactorTo != testEmail {
		t.Fatalf("mailer got to=%q code=%q", mailer.twoFactorTo, mailer.twoFactorCode)
	}

	// The code must not travel in the response body.
	if body := challenge.DeviceToken; body == mailer.twoFactorCode {
		t.Fatal("the emailed code leaked into the response")
	}

	verify := request(t, http.MethodPost, "/v1/sessions/verify",
		`{"email":"`+testEmail+`","code":"`+mailer.twoFactorCode+`"}`)
	verify.Header.Set("X-Device-Token", challenge.DeviceToken)

	rec := do(t, s.http.Handler, verify)
	if rec.Code != http.StatusCreated {
		t.Fatalf("verify status = %d body %s", rec.Code, rec.Body.Bytes())
	}

	session := decodeSession(t, rec)
	if session.Token == "" {
		t.Fatal("verify issued no token")
	}

	if !session.User.TwoFactorEnabled {
		t.Error("user response does not report two-factor as on")
	}

	// The token works, and the device is trusted from now on.
	me := do(t, s.http.Handler, authed(t, http.MethodGet, "/v1/me", "", session.Token, ""))
	if me.Code != http.StatusOK {
		t.Fatalf("GET /v1/me status = %d", me.Code)
	}

	if after := signInAda(t, s, challenge.DeviceToken, http.StatusCreated); after.TwoFactorRequired {
		t.Fatal("a verified device was challenged again")
	}
}

func TestVerifyRejectsAWrongCode(t *testing.T) {
	s, mailer := newTestServer(t)
	enableTwoFactorOverHTTP(t, s)

	challenge := signInAda(t, s, "", http.StatusAccepted)

	wrong := "000000"
	if wrong == mailer.twoFactorCode {
		wrong = "111111"
	}

	verify := request(t, http.MethodPost, "/v1/sessions/verify",
		`{"email":"`+testEmail+`","code":"`+wrong+`"}`)
	verify.Header.Set("X-Device-Token", challenge.DeviceToken)

	if rec := do(t, s.http.Handler, verify); rec.Code != http.StatusUnauthorized {
		t.Fatalf("verify status = %d, want 401; body %s", rec.Code, rec.Body.Bytes())
	}
}

// TestRevokingADeviceInvalidatesItsTokenImmediately is the reason the device id
// is a claim. Without it, revoking would only take effect when the token
// expired, and "sign this device out" would be a promise the API does not keep.
func TestRevokingADeviceInvalidatesItsTokenImmediately(t *testing.T) {
	s, _ := newTestServer(t)
	registerAda(t, s)

	first := signInAda(t, s, "", http.StatusCreated)
	second := signInAda(t, s, "", http.StatusCreated)

	// Both tokens work to begin with.
	for name, token := range map[string]string{"first": first.Token, "second": second.Token} {
		if rec := do(t, s.http.Handler, authed(t, http.MethodGet, "/v1/me", "", token, "")); rec.Code != http.StatusOK {
			t.Fatalf("%s token status = %d, want 200", name, rec.Code)
		}
	}

	listed := do(t, s.http.Handler, authed(t, http.MethodGet, "/v1/me/devices", "", second.Token, ""))
	if listed.Code != http.StatusOK {
		t.Fatalf("list devices status = %d body %s", listed.Code, listed.Body.Bytes())
	}

	devices := decodeInto[deviceList](t, listed.Body.Bytes())
	if len(devices.Devices) != 2 {
		t.Fatalf("devices = %d, want 2", len(devices.Devices))
	}

	var current, other uuid.UUID

	for _, d := range devices.Devices {
		if d.Current {
			current = d.ID
		} else {
			other = d.ID
		}
	}

	if current == uuid.Nil || other == uuid.Nil {
		t.Fatalf("expected exactly one current device, got %+v", devices.Devices)
	}

	revoked := do(t, s.http.Handler, authed(t, http.MethodDelete, "/v1/me/devices/"+other.String(), "", second.Token, ""))
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d body %s", revoked.Code, revoked.Body.Bytes())
	}

	// The revoked device's token stops working on its very next request.
	if rec := do(t, s.http.Handler, authed(t, http.MethodGet, "/v1/me", "", first.Token, "")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked device token status = %d, want 401", rec.Code)
	}

	// The caller's own token is untouched.
	if rec := do(t, s.http.Handler, authed(t, http.MethodGet, "/v1/me", "", second.Token, "")); rec.Code != http.StatusOK {
		t.Fatalf("own token status = %d, want 200", rec.Code)
	}

	// And that device can no longer sign in at all.
	req := request(t, http.MethodPost, "/v1/sessions", testSignInAda)
	req.Header.Set("X-Device-Token", first.DeviceToken)

	if rec := do(t, s.http.Handler, req); rec.Code != http.StatusForbidden {
		t.Fatalf("revoked device sign-in status = %d, want 403; body %s", rec.Code, rec.Body.Bytes())
	}
}

func TestRevokeRejectsAnotherAccountsDevice(t *testing.T) {
	s, _ := newTestServer(t)
	registerAda(t, s)
	ada := signInAda(t, s, "", http.StatusCreated)

	if rec := postJSON(t, s.http.Handler, "/v1/users",
		`{"name":"Bob","email":"bob@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("create bob status = %d", rec.Code)
	}

	bobIn := postJSON(t, s.http.Handler, "/v1/sessions", `{"email":"bob@example.com","password":"twelve-chars"}`)
	if bobIn.Code != http.StatusCreated {
		t.Fatalf("bob sign-in status = %d", bobIn.Code)
	}

	bobDevices := do(t, s.http.Handler, authed(t, http.MethodGet, "/v1/me/devices", "", decodeSession(t, bobIn).Token, ""))
	bobList := decodeInto[deviceList](t, bobDevices.Body.Bytes())

	if len(bobList.Devices) != 1 {
		t.Fatalf("bob devices = %d, want 1", len(bobList.Devices))
	}

	rec := do(t, s.http.Handler, authed(t, http.MethodDelete,
		"/v1/me/devices/"+bobList.Devices[0].ID.String(), "", ada.Token, ""))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-account revoke status = %d, want 404", rec.Code)
	}
}

func TestLoginEventsRecordSuccessAndFailure(t *testing.T) {
	s, _ := newTestServer(t)
	registerAda(t, s)

	session := signInAda(t, s, "", http.StatusCreated)

	bad := postJSON(t, s.http.Handler, "/v1/sessions", `{"email":"ada@example.com","password":"wrong-password"}`)
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("bad password status = %d, want 401", bad.Code)
	}

	rec := do(t, s.http.Handler, authed(t, http.MethodGet, "/v1/me/login-events", "", session.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("login events status = %d body %s", rec.Code, rec.Body.Bytes())
	}

	events := decodeInto[eventList](t, rec.Body.Bytes())
	if len(events.Events) != 2 {
		t.Fatalf("events = %d, want 2: %s", len(events.Events), rec.Body.Bytes())
	}

	if events.Events[0].Outcome != "bad_password" {
		t.Errorf("newest outcome = %q, want bad_password", events.Events[0].Outcome)
	}

	if events.Events[1].Outcome != "success" {
		t.Errorf("oldest outcome = %q, want success", events.Events[1].Outcome)
	}

	if events.Events[0].IP == "" {
		t.Error("event has no source address")
	}
}

func TestSetTwoFactorNeedsTokenAndPassword(t *testing.T) {
	s, _ := newTestServer(t)
	registerAda(t, s)
	session := signInAda(t, s, "", http.StatusCreated)

	// No bearer token at all.
	anon := do(t, s.http.Handler, request(t, http.MethodPut, "/v1/me/two-factor",
		`{"password":"`+testPassword+`","enabled":true}`))
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", anon.Code)
	}

	// Valid token, wrong password.
	wrong := do(t, s.http.Handler, authed(t, http.MethodPut, "/v1/me/two-factor",
		`{"password":"not-the-password","enabled":true}`, session.Token, session.DeviceToken))
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d, want 401", wrong.Code)
	}

	me := do(t, s.http.Handler, authed(t, http.MethodGet, "/v1/me", "", session.Token, ""))

	var body struct {
		TwoFactorEnabled bool `json:"two_factor_enabled"`
	}
	if err := json.Unmarshal(me.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode me: %v", err)
	}

	if body.TwoFactorEnabled {
		t.Fatal("two-factor was switched on without the password")
	}
}
