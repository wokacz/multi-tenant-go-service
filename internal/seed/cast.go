package seed

import (
	"context"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
)

// CastMember is one documented account.
//
// Each one is a *situation* rather than a person: somebody testing the last-owner
// refusal should sign in as the last owner, not build one. The Situation text is
// what docs/guides/009_seed_data.md prints, so the table in the documentation and
// the data in the database come from one place and cannot drift.
type CastMember struct {
	Handle    string
	Name      string
	Locale    string
	Situation string
}

// Cast is the fixed cast, in the order the documentation lists them.
//
// Their organizations and roles are given to them by the organizations and states
// parts; this is only who they are. Fourteen because that is how many distinct
// situations the rules actually have — adding a fifteenth is adding a rule.
func Cast() []CastMember {
	return []CastMember{
		{"platform", "Pola Platformowa", "pl",
			"Installation administrator: platform_admin, plus owner of seed-acme"},
		{"owner", "Olga Owner", "pl",
			"Owner of seed-acme, the organization with enough members to page through"},
		{"lastowner", "Lars Lastowner", "en",
			"The only owner of seed-solo: leaving and demotion are both refused for them"},
		{"admin", "Ada Adminowa", "pl",
			"Administrator of seed-acme: manages members and roles, cannot delete the organization"},
		{"inviter", "Iwo Inviter", "pl",
			"Holds members.invite only, through a custom role: may send and withdraw invitations, may not remove anybody"},
		{"remover", "Rita Remover", "en",
			"Holds members.remove only, through a custom role: the other side of the A6 split"},
		{"member", "Marek Member", "pl", "Plain member of seed-acme"},
		{"viewer", "Wiktor Viewer", "pl", "Read-only in seed-acme"},
		{"suspended", "Zofia Suspended", "pl",
			"Suspended in seed-acme: still a member, every permission withdrawn"},
		{"twofactor", "Tomasz Twofactor", "pl",
			"Two-factor enabled: signing in returns 202 and emails a code"},
		{"multiorg", "Maja Multiorg", "pl",
			"Member of seed-acme, seed-globex and seed-solo, with a different role in each"},
		{"invited", "Ignacy Invited", "pl",
			"Has an account and a pending invitation to seed-globex, not yet accepted"},
		{"nowhere", "Nina Nowhere", "pl",
			"Belongs to no organization: left everything, which is what a leaver looks like afterwards"},
		{"changing", "Cezary Changing", "pl",
			"Has a pending email change: the code was issued and never confirmed"},
	}
}

// cast creates the documented accounts and nothing else.
type cast struct{}

func (cast) Name() string { return "cast" }

func (c cast) Run(ctx context.Context, w *World) error {
	for _, member := range Cast() {
		if _, err := w.ensureAccount(ctx, member.Handle, member.Name, member.Locale); err != nil {
			return err
		}
	}

	return nil
}

// custom roles the cast needs, so that "may invite but not remove" is a state
// somebody can sign into rather than a paragraph in a document.
var customRoles = []struct {
	Key         string
	Name        string
	Description string
	Permissions []authz.Permission
}{
	{
		Key:         "inviter",
		Name:        "Zapraszający",
		Description: "Może wysyłać i wycofywać zaproszenia, nie może nikogo usuwać",
		Permissions: []authz.Permission{
			authz.PermOrganizationRead,
			authz.PermMembersRead,
			authz.PermMembersInvite,
		},
	},
	{
		Key:         "remover",
		Name:        "Usuwający",
		Description: "Może usuwać członków, nie może zapraszać",
		Permissions: []authz.Permission{
			authz.PermOrganizationRead,
			authz.PermMembersRead,
			authz.PermMembersRemove,
		},
	},
}
