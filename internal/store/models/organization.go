package models

import (
	"fmt"
	"unicode/utf8"

	"gorm.io/gorm"
)

// DefaultOrganizationSlug names the organization every account joins when the
// installation does not need more than one. It is created by migration and
// carries IsProtected, so the ordinary delete path refuses it: an installation
// that lost its only organization has no working accounts and no UI to fix that
// from.
const DefaultOrganizationSlug = "default"

// MaxSlugLength matches the column. 63 is the longest label a DNS name allows,
// which is the ceiling a slug would hit first if these ever become subdomains.
const MaxSlugLength = 63

type Organization struct {
	Model
	SoftDelete

	// Slug is the human-readable handle. It is unique and stable, but it is
	// deliberately *not* how the API addresses an organization — paths use the
	// uuid, because a slug is guessable and would turn the tenant list into
	// something anyone can enumerate.
	// Unique only among live organizations, for the same reason as users.email: a
	// deleted one held its slug for ever and creating it again answered
	// 409 slug_taken with nothing to explain it.
	//
	// It is safe here because nothing addresses an organization by slug — every
	// route takes the id. The slug is a label and a lookup for the default
	// organization, so reusing one cannot make an old link resolve to a different
	// tenant.
	Slug string `gorm:"size:63;not null;index:idx_organizations_slug,unique,where:deleted_at IS NULL"`
	Name string `gorm:"size:100;not null"`

	// OnDelete:CASCADE only fires on a hard delete. An organization is soft
	// deleted, so every query that reads through these has to filter
	// organizations.deleted_at itself — see the repository.
	Memberships []Membership `gorm:"constraint:OnDelete:CASCADE"`
	Roles       []Role       `gorm:"constraint:OnDelete:CASCADE"`
}

// BeforeSave keeps a malformed slug out of the database. The rules are narrow on
// purpose: a slug appears in URLs a customer will paste into tickets, and
// anything needing percent-encoding there is a slug that will be transcribed
// wrongly.
func (o *Organization) BeforeSave(_ *gorm.DB) error {
	if !validSlug(o.Slug) {
		return fmt.Errorf("models: invalid organization slug %q", o.Slug)
	}

	if o.Name == "" || utf8.RuneCountInString(o.Name) > 100 {
		return fmt.Errorf("models: invalid organization name %q", o.Name)
	}

	return nil
}

// validSlug allows lowercase letters, digits and single inner hyphens.
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
