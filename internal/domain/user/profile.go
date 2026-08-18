package user

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

// UpdateProfile changes the two things an account owner may change about
// themselves without proving anything: their display name and their language.
//
// Neither is a credential, so neither asks for the password. The address does —
// BeginEmailChange requires it — because an address is how the account is
// recovered, and a name is not.
//
// A nil pointer means the request said nothing about that field, which is not the
// same as sending an empty one: an empty locale is a real value meaning "no
// preference, negotiate per request", and an empty name is refused. That
// distinction is why this takes pointers instead of two strings with a convention
// on top.
//
// The account is read first and the fields it did not mention are carried over, so
// the repository writes both columns every time and there is no partial-update
// mode in the store to get wrong. Two concurrent edits therefore end with the
// second one's values, which is what "last writer wins" means and is fine for a
// profile: nothing here is a decision anybody else depends on.
func (s *Service) UpdateProfile(ctx context.Context, userID uuid.UUID, name, locale *string) (*ent.User, error) {
	current, err := s.repo.ByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	nextName := current.Name
	if name != nil {
		nextName = strings.TrimSpace(*name)
		if nextName == "" {
			return nil, ErrNameEmpty
		}

		if utf8.RuneCountInString(nextName) > MaxNameLength {
			return nil, ErrNameTooLong
		}
	}

	nextLocale := current.Locale
	if locale != nil {
		// Already resolved against the shipped catalog by the caller — the tag is
		// stored as the catalog spells it, so pl-PL is kept as pl rather than as a
		// second spelling of the same language.
		nextLocale = *locale
	}

	if err := s.repo.UpdateProfile(ctx, userID, nextName, nextLocale); err != nil {
		return nil, err
	}

	current.Name = nextName
	current.Locale = nextLocale

	return current, nil
}
