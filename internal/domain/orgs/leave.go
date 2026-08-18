package orgs

import (
	"context"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

// Leave takes the caller out of one of their organizations.
//
// It is self-service, and that is the whole point: until this existed the only
// ways out were asking an administrator or calling remove-member on yourself,
// which needs members.remove — so the people most likely to want out, the ones
// with no permissions at all, were the ones who could not.
//
// The membership is named rather than the organization. The caller's own list is
// where the id comes from, and looking it up there is also the authorization: a
// membership that is not in it is not theirs, and answers ErrNotFound whether it
// belongs to somebody else, sits in a deleted organization, or does not exist.
// Nothing here takes an organization id from the request, so there is no shape in
// which one could reach a scoped repository call without having been checked.
//
// The last-owner rule applies unchanged. Somebody has to be able to administer an
// organization, and "I left" is not a reason to make an exception — the refusal
// tells them to appoint another owner first, which is a thing they can do.
func (s *Service) Leave(ctx context.Context, userID, membershipID uuid.UUID) error {
	mine, err := s.dir.MembershipsForUser(ctx, userID)
	if err != nil {
		return err
	}

	for _, membership := range mine {
		if membership.ID != membershipID {
			continue
		}

		// ActionMemberLeft, not ActionMemberRemoved: the row disappears the same
		// way, and the history should still say which of the two happened.
		return s.repo.RemoveMember(ctx, membership.Organization.ID, membershipID,
			ent.ActionMemberLeft, RefuseLastOwnerLoss(true))
	}

	return ErrNotFound
}
