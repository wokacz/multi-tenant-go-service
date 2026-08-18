package httptest

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api"
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
func enableTwoFactorOverHTTP(t *testing.T, s *api.Server) SessionBody {
	t.Helper()

	RegisterAda(t, s)
	first := SignInAda(t, s, "", http.StatusCreated)

	rec := Do(t, s.Handler(), Authed(t, http.MethodPut, "/v1/me/two-factor",
		`{"password":"`+TestPassword+`","enabled":true}`, first.Token, first.DeviceToken))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("enable two-factor status = %d body %s", rec.Code, rec.Body.Bytes())
	}

	return first
}

func TestTwoFactorSignInOverHTTP(t *testing.T) {
	s, mailer := NewTestServer(t)
	trusted := enableTwoFactorOverHTTP(t, s)

	if again := SignInAda(t, s, trusted.DeviceToken, http.StatusCreated); again.TwoFactorRequired {
		t.Fatal("the enrolling device was challenged")
	}

	challenge := SignInAda(t, s, "", http.StatusAccepted)

	if !challenge.TwoFactorRequired {
		t.Fatal("two_factor_required is false on a 202")
	}

	if challenge.Token != "" {
		t.Fatal("a token was issued alongside the challenge")
	}

	if challenge.DeviceToken == "" {
		t.Fatal("the challenge did not hand out a device token")
	}

	if mailer.TwoFactorCode == "" || mailer.TwoFactorTo != TestEmail {
		t.Fatalf("mailer got to=%q code=%q", mailer.TwoFactorTo, mailer.TwoFactorCode)
	}

	if body := challenge.DeviceToken; body == mailer.TwoFactorCode {
		t.Fatal("the emailed code leaked into the response")
	}

	verify := Request(t, http.MethodPost, "/v1/sessions/verify",
		`{"email":"`+TestEmail+`","code":"`+mailer.TwoFactorCode+`"}`)
	verify.Header.Set("X-Device-Token", challenge.DeviceToken)

	rec := Do(t, s.Handler(), verify)
	if rec.Code != http.StatusCreated {
		t.Fatalf("verify status = %d body %s", rec.Code, rec.Body.Bytes())
	}

	session := DecodeSession(t, rec)
	if session.Token == "" {
		t.Fatal("verify issued no token")
	}

	if !session.User.TwoFactorEnabled {
		t.Error("user response does not report two-factor as on")
	}

	me := Do(t, s.Handler(), Authed(t, http.MethodGet, "/v1/me", "", session.Token, ""))
	if me.Code != http.StatusOK {
		t.Fatalf("GET /v1/me status = %d", me.Code)
	}

	if after := SignInAda(t, s, challenge.DeviceToken, http.StatusCreated); after.TwoFactorRequired {
		t.Fatal("a verified device was challenged again")
	}
}

func TestVerifyRejectsAWrongCode(t *testing.T) {
	s, mailer := NewTestServer(t)
	enableTwoFactorOverHTTP(t, s)

	challenge := SignInAda(t, s, "", http.StatusAccepted)

	wrong := "000000"
	if wrong == mailer.TwoFactorCode {
		wrong = "111111"
	}

	verify := Request(t, http.MethodPost, "/v1/sessions/verify",
		`{"email":"`+TestEmail+`","code":"`+wrong+`"}`)
	verify.Header.Set("X-Device-Token", challenge.DeviceToken)

	if rec := Do(t, s.Handler(), verify); rec.Code != http.StatusUnauthorized {
		t.Fatalf("verify status = %d, want 401; body %s", rec.Code, rec.Body.Bytes())
	}
}

func TestRevokingADeviceInvalidatesItsTokenImmediately(t *testing.T) {
	s, _ := NewTestServer(t)
	RegisterAda(t, s)

	first := SignInAda(t, s, "", http.StatusCreated)
	second := SignInAda(t, s, "", http.StatusCreated)

	for name, token := range map[string]string{"first": first.Token, "second": second.Token} {
		if rec := Do(t, s.Handler(), Authed(t, http.MethodGet, "/v1/me", "", token, "")); rec.Code != http.StatusOK {
			t.Fatalf("%s token status = %d, want 200", name, rec.Code)
		}
	}

	listed := Do(t, s.Handler(), Authed(t, http.MethodGet, "/v1/me/devices", "", second.Token, ""))
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

	revoked := Do(t, s.Handler(), Authed(t, http.MethodDelete, "/v1/me/devices/"+other.String(), "", second.Token, ""))
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d body %s", revoked.Code, revoked.Body.Bytes())
	}

	if rec := Do(t, s.Handler(), Authed(t, http.MethodGet, "/v1/me", "", first.Token, "")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked device token status = %d, want 401", rec.Code)
	}

	if rec := Do(t, s.Handler(), Authed(t, http.MethodGet, "/v1/me", "", second.Token, "")); rec.Code != http.StatusOK {
		t.Fatalf("own token status = %d, want 200", rec.Code)
	}

	req := Request(t, http.MethodPost, "/v1/sessions", TestSignInAda)
	req.Header.Set("X-Device-Token", first.DeviceToken)

	if rec := Do(t, s.Handler(), req); rec.Code != http.StatusForbidden {
		t.Fatalf("revoked device sign-in status = %d, want 403; body %s", rec.Code, rec.Body.Bytes())
	}
}

func TestRevokeRejectsAnotherAccountsDevice(t *testing.T) {
	s, _ := NewTestServer(t)
	RegisterAda(t, s)
	ada := SignInAda(t, s, "", http.StatusCreated)

	if rec := PostJSON(t, s.Handler(), "/v1/users",
		`{"name":"Bob","email":"bob@example.com","password":"twelve-chars","password_confirm":"twelve-chars"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("create bob status = %d", rec.Code)
	}

	bobIn := PostJSON(t, s.Handler(), "/v1/sessions", `{"email":"bob@example.com","password":"twelve-chars"}`)
	if bobIn.Code != http.StatusCreated {
		t.Fatalf("bob sign-in status = %d", bobIn.Code)
	}

	bobDevices := Do(t, s.Handler(), Authed(t, http.MethodGet, "/v1/me/devices", "", DecodeSession(t, bobIn).Token, ""))
	bobList := decodeInto[deviceList](t, bobDevices.Body.Bytes())

	if len(bobList.Devices) != 1 {
		t.Fatalf("bob devices = %d, want 1", len(bobList.Devices))
	}

	rec := Do(t, s.Handler(), Authed(t, http.MethodDelete,
		"/v1/me/devices/"+bobList.Devices[0].ID.String(), "", ada.Token, ""))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-account revoke status = %d, want 404", rec.Code)
	}
}

func TestLoginEventsRecordSuccessAndFailure(t *testing.T) {
	s, _ := NewTestServer(t)
	RegisterAda(t, s)

	session := SignInAda(t, s, "", http.StatusCreated)

	bad := PostJSON(t, s.Handler(), "/v1/sessions", `{"email":"ada@example.com","password":"wrong-password"}`)
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("bad password status = %d, want 401", bad.Code)
	}

	rec := Do(t, s.Handler(), Authed(t, http.MethodGet, "/v1/me/login-events", "", session.Token, ""))
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
	s, _ := NewTestServer(t)
	RegisterAda(t, s)
	session := SignInAda(t, s, "", http.StatusCreated)

	anon := Do(t, s.Handler(), Request(t, http.MethodPut, "/v1/me/two-factor",
		`{"password":"`+TestPassword+`","enabled":true}`))
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", anon.Code)
	}

	wrong := Do(t, s.Handler(), Authed(t, http.MethodPut, "/v1/me/two-factor",
		`{"password":"not-the-password","enabled":true}`, session.Token, session.DeviceToken))
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d, want 401", wrong.Code)
	}

	me := Do(t, s.Handler(), Authed(t, http.MethodGet, "/v1/me", "", session.Token, ""))

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
