package api

import (
	"net/http"
	"testing"
)

// userBody is the account as GET /v1/me and PATCH /v1/me return it.
type userBody struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Locale string `json:"locale"`
}

func (f *authzFixture) me(t *testing.T) userBody {
	t.Helper()

	var body userBody
	f.call(t, http.MethodGet, "/v1/me", "").expect(t, http.StatusOK).decode(t, &body)

	return body
}

// TestChangingTheOwnNameNeedsNoPassword is the line between a profile field and a
// credential. The address is recovery material and POST /v1/me/email asks for the
// password before it will move; a display name is not, so this does not.
func TestChangingTheOwnNameNeedsNoPassword(t *testing.T) {
	f := newAuthzFixture(t)

	var updated userBody
	f.call(t, http.MethodPatch, "/v1/me", `{"name":"Ada Lovelace"}`).
		expect(t, http.StatusOK).decode(t, &updated)

	if updated.Name != "Ada Lovelace" {
		t.Errorf("the response says name = %q", updated.Name)
	}

	if got := f.me(t).Name; got != "Ada Lovelace" {
		t.Errorf("GET /v1/me says name = %q; the response described a change that "+
			"was not stored", got)
	}
}

// TestAnAbsentFieldIsLeftAlone is why the request uses pointers.
//
// A struct of plain strings cannot tell "do not touch the name" from "set the name
// to empty", and the second reading would wipe a field the client never mentioned.
func TestAnAbsentFieldIsLeftAlone(t *testing.T) {
	f := newAuthzFixture(t)

	f.call(t, http.MethodPatch, "/v1/me", `{"name":"Ada Lovelace"}`).
		expect(t, http.StatusOK)
	f.call(t, http.MethodPatch, "/v1/me", `{"locale":"pl"}`).
		expect(t, http.StatusOK)

	after := f.me(t)

	if after.Name != "Ada Lovelace" {
		t.Errorf("name = %q after a request that only mentioned the locale", after.Name)
	}

	if after.Locale != "pl" {
		t.Errorf("locale = %q, want pl", after.Locale)
	}
}

// TestAWhitespaceNameIsRefused covers the gap between the schema and the rule.
// minLength stops "", and only the service stops "   " — which would otherwise be
// stored and render as a nameless account everywhere.
func TestAWhitespaceNameIsRefused(t *testing.T) {
	f := newAuthzFixture(t)

	var doc problemBody
	f.call(t, http.MethodPatch, "/v1/me", `{"name":"   "}`).
		expect(t, http.StatusUnprocessableEntity).decode(t, &doc)

	if doc.Code != "name_empty" {
		t.Errorf("code = %q, want name_empty", doc.Code)
	}
}

// TestSettingTheLanguageChangesTheNextResponse is the point of storing a locale at
// all: the account's own choice outranks Accept-Language from then on.
func TestSettingTheLanguageChangesTheNextResponse(t *testing.T) {
	f := newAuthzFixture(t)

	f.call(t, http.MethodPatch, "/v1/me", `{"locale":"pl"}`).expect(t, http.StatusOK)

	// An English header, and a request that fails so there is a translated detail
	// to read. The stored preference has to win.
	req := authed(t, http.MethodGet, "/v1/orgs/018f0000-0000-7000-8000-000000000000",
		"", f.token, "")
	req.Header.Set("Accept-Language", "en")

	rec := do(t, f.server.http.Handler, req)
	if got := rec.Header().Get("Content-Language"); got != "pl" {
		t.Errorf("Content-Language = %q, want pl — the stored choice outranks the header", got)
	}
}

// TestClearingTheLanguageGoesBackToTheHeader is the other half, and the reason the
// empty string had to stay tellable from an absent field: without it, a language
// once chosen could never be handed back to the browser.
func TestClearingTheLanguageGoesBackToTheHeader(t *testing.T) {
	f := newAuthzFixture(t)

	f.call(t, http.MethodPatch, "/v1/me", `{"locale":"pl"}`).expect(t, http.StatusOK)
	f.call(t, http.MethodPatch, "/v1/me", `{"locale":""}`).expect(t, http.StatusOK)

	if got := f.me(t).Locale; got != "" {
		t.Fatalf("locale = %q, want it cleared", got)
	}

	req := authed(t, http.MethodGet, "/v1/orgs/018f0000-0000-7000-8000-000000000000",
		"", f.token, "")
	req.Header.Set("Accept-Language", "en")

	rec := do(t, f.server.http.Handler, req)
	if got := rec.Header().Get("Content-Language"); got != "en" {
		t.Errorf("Content-Language = %q, want en — nothing is stored, so the header "+
			"decides again", got)
	}
}

// TestARegionalTagIsStoredAsTheCatalogSpellsIt keeps one spelling per language in
// the column. A browser sending pl-PL, pl-pl and pl means one preference, not
// three, and Negotiate would resolve all of them to pl anyway.
func TestARegionalTagIsStoredAsTheCatalogSpellsIt(t *testing.T) {
	f := newAuthzFixture(t)

	var updated userBody
	f.call(t, http.MethodPatch, "/v1/me", `{"locale":"pl-PL"}`).
		expect(t, http.StatusOK).decode(t, &updated)

	if updated.Locale != "pl" {
		t.Errorf("locale = %q, want pl", updated.Locale)
	}
}

// TestAnUnshippedLanguageIsRefused is the difference between Resolve and
// Negotiate. Rendering a response falls back to English when nothing matches,
// because something has to be written; remembering a preference must not, or the
// caller is silently given a language they did not ask for, permanently.
func TestAnUnshippedLanguageIsRefused(t *testing.T) {
	f := newAuthzFixture(t)

	for _, tag := range []string{"de", "klingon", "!!!"} {
		t.Run(tag, func(t *testing.T) {
			var doc problemBody
			f.call(t, http.MethodPatch, "/v1/me", `{"locale":"`+tag+`"}`).
				expect(t, http.StatusUnprocessableEntity).decode(t, &doc)

			if doc.Code != "unsupported_locale" {
				t.Errorf("code = %q, want unsupported_locale", doc.Code)
			}
		})
	}
}
