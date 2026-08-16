package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wokacz/go-example/internal/domain/authz"
	"github.com/wokacz/go-example/internal/i18n"
	"github.com/wokacz/go-example/internal/store/repositories/memory"
)

// TestTheCodeIsStableAcrossLanguages is the contract the frontend is built on.
//
// A client keys its own wording off code and never parses detail. If the two
// moved together, every translation would be a breaking change for anyone who
// had branched on the message text.
func TestTheCodeIsStableAcrossLanguages(t *testing.T) {
	f := newAuthzFixture(t)

	role := f.repo.SeedRole(f.orgID, "auditors", string(authz.PermMembersRead))
	f.repo.SeedMemberRoles(f.membership, role)

	details := map[string]string{}

	for _, language := range []string{"en", "pl"} {
		req := authed(t, http.MethodGet, "/v1/orgs/"+f.orgID.String(), "", f.token, "")
		req.Header.Set("Accept-Language", language)

		rec := do(t, f.server.http.Handler, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body %s", rec.Code, rec.Body.Bytes())
		}

		body := decodeProblem(t, rec.Body.Bytes())

		if body.Code != "forbidden_requires" {
			t.Errorf("%s: code = %q, want it unchanged by the language", language, body.Code)
		}

		if body.RequiredPermission != string(authz.PermOrganizationRead) {
			t.Errorf("%s: required_permission = %q, want the raw key",
				language, body.RequiredPermission)
		}

		if got := rec.Header().Get("Content-Language"); got != language {
			t.Errorf("%s: Content-Language = %q, want %q", language, got, language)
		}

		details[language] = body.Detail
	}

	if details["en"] == details["pl"] {
		t.Errorf("both languages produced %q; the detail is not actually translated", details["en"])
	}
}

// TestAnUnknownLanguageFallsBackRatherThanFailing keeps a request with an exotic
// Accept-Language from becoming an error of its own.
func TestAnUnknownLanguageFallsBackRatherThanFailing(t *testing.T) {
	s, _ := newTestServer(t)

	for _, header := range []string{"de", "!!!", "de;q=0.9, fr;q=0.8", ""} {
		t.Run("Accept-Language: "+header, func(t *testing.T) {
			req := request(t, http.MethodGet, "/v1/me", "")
			if header != "" {
				req.Header.Set("Accept-Language", header)
			}

			rec := do(t, s.http.Handler, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}

			if got := rec.Header().Get("Content-Language"); got != string(i18n.Fallback) {
				t.Errorf("Content-Language = %q, want %q", got, i18n.Fallback)
			}

			body := decodeProblem(t, rec.Body.Bytes())
			if body.Code != "unauthorized" {
				t.Errorf("code = %q, want unauthorized", body.Code)
			}
		})
	}
}

// TestVaryIsSetOnEveryResponse matters for anything caching between the client
// and the process. Without it a shared cache serves one language's body to a
// client that asked for another.
func TestVaryIsSetOnEveryResponse(t *testing.T) {
	s, _ := newTestServer(t)

	for _, path := range []string{"/health", "/v1/me", "/v1/nothing-here"} {
		t.Run(path, func(t *testing.T) {
			rec := do(t, s.http.Handler, request(t, http.MethodGet, path, ""))

			if !strings.Contains(rec.Header().Get("Vary"), "Accept-Language") {
				t.Errorf("Vary = %q, want it to name Accept-Language", rec.Header().Get("Vary"))
			}
		})
	}
}

// TestTheRoutersOwnErrorsAreTranslatedToo covers the responses chi writes before
// huma is involved. A Polish API answering 404 in English is the kind of gap
// that is invisible in development.
func TestTheRoutersOwnErrorsAreTranslatedToo(t *testing.T) {
	s, _ := newTestServer(t)

	req := request(t, http.MethodGet, "/v1/nothing-here", "")
	req.Header.Set("Accept-Language", "pl")

	rec := do(t, s.http.Handler, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	body := decodeProblem(t, rec.Body.Bytes())

	if body.Code != "no_operation" {
		t.Errorf("code = %q, want no_operation", body.Code)
	}

	want := i18n.Default().T("pl", "error.no_operation", "/v1/nothing-here")
	if body.Detail != want {
		t.Errorf("detail = %q, want %q", body.Detail, want)
	}
}

// TestTheAccountsLanguageOutranksTheHeader is why User.Locale exists. Somebody
// who signed up in Polish should not get English errors because they opened the
// product on a machine that was installed in English.
func TestTheAccountsLanguageOutranksTheHeader(t *testing.T) {
	mailer := &capturingMailer{}
	s, _ := newTestAPIConfig(t, mailer, memory.NewUsers(), nil)

	// Register with Accept-Language: pl, which is what the account remembers.
	req := request(t, http.MethodPost, "/v1/users", testRegisterAda)
	req.Header.Set("Accept-Language", "pl")

	if rec := do(t, s.http.Handler, req); rec.Code != http.StatusNoContent {
		t.Fatalf("register status = %d, want 204; body %s", rec.Code, rec.Body.Bytes())
	}

	session := signInAda(t, s, "", http.StatusCreated)

	// Now ask for something that does not exist, with an English header. The
	// stored preference must win.
	authedReq := authed(t, http.MethodGet, "/v1/orgs/018f0000-0000-7000-8000-000000000000",
		"", session.Token, "")
	authedReq.Header.Set("Accept-Language", "en")

	rec := do(t, s.http.Handler, authedReq)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body %s", rec.Code, rec.Body.Bytes())
	}

	if got := rec.Header().Get("Content-Language"); got != "pl" {
		t.Errorf("Content-Language = %q, want pl — the account's own choice", got)
	}

	body := decodeProblem(t, rec.Body.Bytes())
	if body.Detail != i18n.Default().T("pl", "error.not_found") {
		t.Errorf("detail = %q, want the Polish message", body.Detail)
	}
}

// TestRegistrationRemembersTheLanguage is the other half: the preference has to
// get stored in the first place, and it is captured rather than asked for.
func TestRegistrationRemembersTheLanguage(t *testing.T) {
	mailer := &capturingMailer{}
	s, _ := newTestAPIConfig(t, mailer, memory.NewUsers(), nil)

	req := request(t, http.MethodPost, "/v1/users", testRegisterAda)
	req.Header.Set("Accept-Language", "pl-PL")

	if rec := do(t, s.http.Handler, req); rec.Code != http.StatusNoContent {
		t.Fatalf("register status = %d, want 204", rec.Code)
	}

	session := signInAda(t, s, "", http.StatusCreated)

	rec := do(t, s.http.Handler, authed(t, http.MethodGet, "/v1/me", "", session.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var me struct {
		Locale string `json:"locale"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.Bytes())
	}

	// pl-PL narrows to the pl catalog on the way in, so what is stored is a
	// language the process can actually render.
	if me.Locale != "pl" {
		t.Errorf("locale = %q, want pl", me.Locale)
	}
}

// TestValidationFailuresCarryACodeToo covers the path huma writes itself, which
// never passes through problem.Error.
func TestValidationFailuresCarryACodeToo(t *testing.T) {
	s, _ := newTestServer(t)

	rec := postJSON(t, s.http.Handler, "/v1/users", `{"name":"Ada"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body %s", rec.Code, rec.Body.Bytes())
	}

	body := decodeProblem(t, rec.Body.Bytes())
	if body.Code != "validation_failed" {
		t.Errorf("code = %q, want validation_failed", body.Code)
	}
}
