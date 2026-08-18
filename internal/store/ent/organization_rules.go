package ent

import (
	"fmt"
	"time"
	"unicode/utf8"
)

// DefaultOrganizationSlug names the organization every account joins when the
// installation does not need more than one. It is created by migration and
// carries IsProtected, so the ordinary delete path refuses it.
const DefaultOrganizationSlug = "default"

// MaxSlugLength matches the column. 63 is the longest label a DNS name allows.
const MaxSlugLength = 63

func (o *Organization) IsDeleted() bool {
	return o.DeletedAt != nil
}

func (o *Organization) RefuseIfProtected() error {
	if o.IsProtected {
		return ErrProtected
	}

	return nil
}

func (o *Organization) Delete() error {
	if err := o.RefuseIfProtected(); err != nil {
		return err
	}

	now := time.Now().UTC()
	o.DeletedAt = &now

	return nil
}

func (o *Organization) Restore() {
	o.DeletedAt = nil
}

func (o *Organization) Validate() error {
	if !validSlug(o.Slug) {
		return fmt.Errorf("ent: invalid organization slug %q", o.Slug)
	}

	if o.Name == "" || utf8.RuneCountInString(o.Name) > 100 {
		return fmt.Errorf("ent: invalid organization name %q", o.Name)
	}

	return nil
}

func validSlug(slug string) bool {
	if slug == "" || len(slug) > MaxSlugLength {
		return false
	}

	if slug[0] == '-' || slug[len(slug)-1] == '-' {
		return false
	}

	for i := range len(slug) {
		c := slug[i]

		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-':
			if slug[i-1] == '-' {
				return false
			}
		default:
			return false
		}
	}

	return true
}
