package seed

import (
	"context"
	"log/slog"
	"strings"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
)

// Reset removes what the seeder made, and only that.
//
// It works because both names are freed by a soft delete: the partial unique indexes
// on users.email and organizations.slug cover live rows only, so seeding again after
// a reset gets the same addresses and the same slugs rather than colliding with the
// rows it just retired. Without that this would have to hard delete, and a hard
// delete in a tool anybody can run is a different kind of tool.
//
// It never touches the default organization, which is protected, or an account
// outside the seed domain — Guard has already refused if one exists, and -force is
// not a licence to delete somebody's data.
func Reset(ctx context.Context, w *World) error {
	suffix := "@" + Domain

	deletedAccounts := 0

	for {
		page, err := w.UserRepo.All(ctx, 100, 0)
		if err != nil {
			return err
		}

		found := 0

		for i := range page {
			if !strings.HasSuffix(page[i].Email, suffix) {
				continue
			}

			// Deleting an account revokes its devices and leaves its memberships
			// behind, which is the same shape the abandoned organization relies on.
			// A reset therefore does the organizations second.
			if err := w.UserRepo.Delete(ctx, page[i].ID); err != nil {
				return err
			}

			found++
			deletedAccounts++
		}

		// The listing hides deleted accounts, so the page shrinks as this walks it.
		// Stop when a full pass found nothing left to delete.
		if found == 0 {
			break
		}
	}

	deletedOrgs := 0

	for offset := 0; ; {
		page, err := w.Prov.AllOrganizations(ctx, orgs.OrganizationFilter{}, orgs.MaxOrganizationPage, offset)
		if err != nil {
			return err
		}

		if len(page) == 0 {
			break
		}

		removed := 0

		for i := range page {
			if !strings.HasPrefix(page[i].Slug, SlugPrefix) {
				continue
			}

			if err := w.Orgs.DeleteOrganizationByID(ctx, page[i].ID); err != nil {
				// The default organization is protected and never carries the
				// prefix, so this is a real failure rather than a case to skip.
				return err
			}

			removed++
			deletedOrgs++
		}

		if removed == 0 {
			offset += len(page)
		}
	}

	w.Log.Info("reset",
		slog.Int("accounts", deletedAccounts),
		slog.Int("organizations", deletedOrgs))

	return nil
}
