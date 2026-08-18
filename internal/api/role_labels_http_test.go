package api

import (
	"net/http"
	"testing"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/i18n"
)

// TestAShippedRoleIsNamedInTheCallersLanguage is the bug this closes.
//
// The catalog has had role.owner.name in both languages from the start, complete and
// tested, and nothing read it: every response carried roles.name — a column written
// once when the organization was created, from the English string in the Go
// definition. A Polish client listing its roles was told "Owner".
func TestAShippedRoleIsNamedInTheCallersLanguage(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	for _, language := range []string{"en", "pl"} {
		t.Run(language, func(t *testing.T) {
			req := authed(t, http.MethodGet, f.orgPath("/roles"), "", f.token, "")
			req.Header.Set("Accept-Language", language)

			rec := do(t, f.server.http.Handler, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body %s", rec.Code, rec.Body.Bytes())
			}

			var body struct {
				Roles []roleBody `json:"roles"`
			}
			(&httpResult{code: rec.Code, body: rec.Body.Bytes()}).decode(t, &body)

			want := i18n.Default().T(i18n.Locale(language), "role.owner.name")

			found := false

			for _, role := range body.Roles {
				if role.Key != string(authz.RoleOwner) {
					continue
				}

				found = true

				if role.Name != want {
					t.Errorf("name = %q, want %q", role.Name, want)
				}
			}

			if !found {
				t.Fatalf("the owner role is not in the listing")
			}
		})
	}

	// The two languages have to actually differ, or the assertion above would hold
	// for a response that ignored the language entirely.
	if i18n.Default().T("en", "role.owner.name") == i18n.Default().T("pl", "role.owner.name") {
		t.Error("both catalogs name the owner role the same; this test cannot fail")
	}
}

// TestACustomRoleKeepsTheNameItWasGiven is the other half of the decision.
//
// A name somebody typed in the settings screen is already in the language they work
// in. Translating it would mean a second copy in the database — which is what
// role_translations was for, and nothing ever wrote to it or read from it.
func TestACustomRoleKeepsTheNameItWasGiven(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	var created roleBody
	f.call(t, http.MethodPost, f.orgPath("/roles"),
		`{"key":"ksiegowosc","name":"Księgowość","description":"Faktury","permissions":[]}`).
		expect(t, http.StatusCreated).decode(t, &created)

	if created.Name != "Księgowość" {
		t.Fatalf("name = %q on creation", created.Name)
	}

	req := authed(t, http.MethodGet, f.orgPath("/roles"), "", f.token, "")
	req.Header.Set("Accept-Language", "en")

	rec := do(t, f.server.http.Handler, req)

	var body struct {
		Roles []roleBody `json:"roles"`
	}
	(&httpResult{code: rec.Code, body: rec.Body.Bytes()}).decode(t, &body)

	for _, role := range body.Roles {
		if role.Key == "ksiegowosc" && role.Name != "Księgowość" {
			t.Errorf("name = %q read back under Accept-Language: en, want it unchanged", role.Name)
		}
	}
}

// TestAMembersRolesAreNamedInTheCallersLanguage covers the second place a role name
// reaches a client. It is a separate response type, built by a separate function,
// which is exactly how one of them gets fixed and the other does not.
func TestAMembersRolesAreNamedInTheCallersLanguage(t *testing.T) {
	f := newAuthzFixture(t, authz.RoleOwner)

	req := authed(t, http.MethodGet, f.orgPath("/members"), "", f.token, "")
	req.Header.Set("Accept-Language", "pl")

	rec := do(t, f.server.http.Handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body %s", rec.Code, rec.Body.Bytes())
	}

	var body struct {
		Members []struct {
			Roles []struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"roles"`
		} `json:"members"`
	}
	(&httpResult{code: rec.Code, body: rec.Body.Bytes()}).decode(t, &body)

	want := i18n.Default().T("pl", "role.owner.name")
	found := false

	for _, member := range body.Members {
		for _, role := range member.Roles {
			if role.Key != string(authz.RoleOwner) {
				continue
			}

			found = true

			if role.Name != want {
				t.Errorf("name = %q, want %q", role.Name, want)
			}
		}
	}

	if !found {
		t.Fatal("the caller's owner role is not in the members listing")
	}
}
