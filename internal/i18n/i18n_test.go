package i18n_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/i18n"
)

// TestEveryPermissionIsTranslatedInEveryLocale is the completeness guard.
//
// Without it, adding a permission ships a settings screen with a blank label in
// every language nobody remembered to update, and the gap is invisible until a
// customer using that language opens the role editor. Here it is a build
// failure naming the missing key.
func TestEveryPermissionIsTranslatedInEveryLocale(t *testing.T) {
	catalog := i18n.Default()

	for _, locale := range catalog.Locales() {
		for _, def := range authz.Catalog() {
			for _, suffix := range []string{".name", ".description"} {
				key := "permission." + string(def.Key) + suffix

				if !catalog.Has(locale, key) {
					t.Errorf("locale %q has no %q", locale, key)
				}
			}
		}
	}
}

// TestEveryShippedRoleIsTranslatedInEveryLocale is the same rule for the roles
// the product defines. Roles created by users are translated in the database
// instead — see models.RoleTranslation.
func TestEveryShippedRoleIsTranslatedInEveryLocale(t *testing.T) {
	catalog := i18n.Default()

	roles := append(authz.OrganizationRoles(), authz.SystemRoles()...)

	for _, locale := range catalog.Locales() {
		for _, role := range roles {
			for _, suffix := range []string{".name", ".description"} {
				key := "role." + string(role.Key) + suffix

				if !catalog.Has(locale, key) {
					t.Errorf("locale %q has no %q", locale, key)
				}
			}
		}
	}
}

// TestEveryLocaleDefinesTheSameKeys catches the half of the problem the two
// tests above cannot see: an error message, or anything else, present in one
// catalog and missing from another.
//
// It compares against the fallback rather than against a list, so adding a key
// to en.json is what makes the other catalogs fail — which is the direction
// that keeps them honest.
func TestEveryLocaleDefinesTheSameKeys(t *testing.T) {
	catalog := i18n.Default()
	want := catalog.Keys(i18n.Fallback)

	for _, locale := range catalog.Locales() {
		if locale == i18n.Fallback {
			continue
		}

		got := catalog.Keys(locale)

		for _, key := range want {
			if !slices.Contains(got, key) {
				t.Errorf("locale %q is missing %q", locale, key)
			}
		}

		// And nothing extra, which is how a renamed key leaves a translation
		// behind that nothing will ever look up.
		for _, key := range got {
			if !slices.Contains(want, key) {
				t.Errorf("locale %q defines %q, which %q does not", locale, key, i18n.Fallback)
			}
		}
	}
}

// TestPermissionKeysHaveNoOrphanTranslations is the other direction: a
// permission renamed in the catalog leaves its old labels behind, and they will
// never be shown again.
func TestPermissionKeysHaveNoOrphanTranslations(t *testing.T) {
	catalog := i18n.Default()

	known := map[string]bool{}
	for _, def := range authz.Catalog() {
		known[string(def.Key)] = true
	}

	for _, key := range catalog.Keys(i18n.Fallback) {
		rest, ok := strings.CutPrefix(key, "permission.")
		if !ok {
			continue
		}

		permission := strings.TrimSuffix(strings.TrimSuffix(rest, ".name"), ".description")

		if !known[permission] {
			t.Errorf("%q translates %q, which is not in the permission catalog", key, permission)
		}
	}
}

func TestNegotiatePicksTheBestMatch(t *testing.T) {
	catalog := i18n.Default()

	tests := map[string]struct {
		header     string
		preference string
		want       i18n.Locale
	}{
		"exact match":                    {"pl", "", "pl"},
		"regional narrows to base":       {"pl-PL", "", "pl"},
		"unknown language falls back":    {"de", "", i18n.Fallback},
		"empty header falls back":        {"", "", i18n.Fallback},
		"malformed header falls back":    {"!!!", "", i18n.Fallback},
		"stored preference wins":         {"en", "pl", "pl"},
		"unusable preference defers":     {"pl", "de", "pl"},
		"regional preference narrows":    {"en", "pl-PL", "pl"},
		"quality weights are respected":  {"de;q=1.0, pl;q=0.9", "", "pl"},
		"first acceptable wins outright": {"pl, en", "", "pl"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := catalog.Negotiate(tc.header, tc.preference); got != tc.want {
				t.Errorf("Negotiate(%q, %q) = %q, want %q", tc.header, tc.preference, got, tc.want)
			}
		})
	}
}

func TestTranslationFallsBackRatherThanDisappearing(t *testing.T) {
	catalog := i18n.Default()

	t.Run("a regional locale reads its base catalog", func(t *testing.T) {
		got := catalog.T("pl-PL", "error.forbidden")

		if got != catalog.T("pl", "error.forbidden") {
			t.Errorf("T(pl-PL) = %q, want the pl message", got)
		}
	})

	t.Run("an unknown locale reads the fallback", func(t *testing.T) {
		if got := catalog.T("de", "error.forbidden"); got != catalog.T(i18n.Fallback, "error.forbidden") {
			t.Errorf("T(de) = %q, want the fallback message", got)
		}
	})

	// A message that does not exist comes back as its own key. That is
	// deliberate: it shows up in the response as something greppable rather
	// than as a blank field nobody notices.
	t.Run("a missing key returns the key", func(t *testing.T) {
		if got := catalog.T("pl", "error.no_such_message"); got != "error.no_such_message" {
			t.Errorf("T() = %q, want the key itself", got)
		}
	})
}

func TestTranslationsInterpolate(t *testing.T) {
	got := i18n.Default().T("en", "error.forbidden_requires", "View members")

	if !strings.Contains(got, "View members") {
		t.Errorf("T() = %q, want the argument interpolated", got)
	}
}

// TestPolishIsActuallyTranslated guards against a catalog that was copied from
// English and never filled in, which passes every completeness test above.
func TestPolishIsActuallyTranslated(t *testing.T) {
	catalog := i18n.Default()

	same := 0
	keys := catalog.Keys(i18n.Fallback)

	for _, key := range keys {
		if catalog.T("pl", key) == catalog.T("en", key) {
			same++
		}
	}

	// A handful legitimately match — proper nouns, format-only strings. Most
	// must not.
	if same*4 > len(keys) {
		t.Errorf("%d of %d Polish messages are identical to the English ones", same, len(keys))
	}
}

func TestLocaleTravelsOnTheContext(t *testing.T) {
	ctx := i18n.WithLocale(t.Context(), "pl")

	if got := i18n.LocaleFrom(ctx); got != "pl" {
		t.Errorf("LocaleFrom() = %q, want pl", got)
	}

	// Outside a request there is no negotiation to inherit, and losing the
	// message entirely would be worse than losing the translation.
	if got := i18n.LocaleFrom(t.Context()); got != i18n.Fallback {
		t.Errorf("LocaleFrom(empty) = %q, want the fallback", got)
	}
}
