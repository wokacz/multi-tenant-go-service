package v1

import (
	"github.com/wokacz/multi-tenant-go-service/internal/i18n"
)

// roleLabel is the name and description a role is shown under.
//
// A shipped role is rendered from the message catalog, which has role.<key>.name
// and .description for every one of them — kept complete in every language by
// TestEveryShippedRoleIsTranslatedInEveryLocale, and read by nothing until now.
// Every response carried roles.name instead: a column written once, when the
// organization was created, from the English string in the Go definition. A Polish
// client asking for its roles was told "Owner".
//
// A role the customer created is shown exactly as they named it. There is no
// translation for those and deliberately no table for one either — the name
// somebody typed is already in the language they work in, and a second copy of it
// in the database was a feature nothing wrote and nothing read.
//
// The stored column stays as the fallback, for a key the catalog does not know:
// showing "role.auditor.name" to a user is worse than showing a stale label.
func roleLabel(locale i18n.Locale, key, name, description string, isSystem bool) (string, string) {
	if !isSystem {
		return name, description
	}

	catalog := i18n.Default()

	if k := "role." + key + ".name"; catalog.Has(i18n.Fallback, k) {
		name = catalog.T(locale, k)
	}

	if k := "role." + key + ".description"; catalog.Has(i18n.Fallback, k) {
		description = catalog.T(locale, k)
	}

	return name, description
}
