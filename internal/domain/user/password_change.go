package user

import (
	"context"

	"github.com/google/uuid"
)

// ChangePassword replaces the account's password from inside a signed-in session.
//
// The current password is required, and not as ceremony: a bearer token that
// leaked out of a browser must not be enough to set a new password, because that
// is the one change that locks the owner out of their own account. It is the same
// reason BeginEmailChange and SetTwoFactorEnabled ask for it.
//
// Every token dies, the caller's own included. That follows from bumping the
// epoch, and it is the honest behaviour rather than a limitation: somebody
// changing their password either has a reason to think it was known to others, in
// which case every session should end, or they do not, in which case signing in
// again costs them one screen. Keeping this session alive would mean issuing a new
// token here, which would make one endpoint out of two — and the endpoint that
// issues tokens is the one that checks whether a second factor is required.
//
// Nothing is recorded. The login history is about sign-ins, and the audit log is
// about authorization inside an organization; an account-level history is a thing
// this build does not have, and inventing half of it here would be worse than the
// gap.
func (s *Service) ChangePassword(ctx context.Context, userID uuid.UUID, current, password, confirm string) error {
	u, err := s.repo.ByID(ctx, userID)
	if err != nil {
		return err
	}

	if err := s.compareHash(ctx, []byte(u.PasswordHash), []byte(current)); err != nil {
		// A cancelled context is not a wrong password. Reporting it as one would
		// tell the caller their password is wrong because the process was shutting
		// down.
		if ctx.Err() != nil {
			return ctx.Err()
		}

		return ErrInvalidCredentials
	}

	if err := requireMatchingPassword(password, confirm); err != nil {
		return err
	}

	// Deliberately no "the new password must differ from the old one". It would
	// need a second bcrypt comparison to enforce and it protects nothing: somebody
	// who re-enters the same password has proved they know it, and the epoch bump
	// still ends every other session, which is the reason they are here.
	hash, err := s.hashedPassword(ctx, password)
	if err != nil {
		return err
	}

	return s.repo.SetPassword(ctx, userID, hash)
}

// SignOutEverywhere invalidates every token issued for the account, including the
// one making the request.
//
// It exists because until now this was only a side effect: changing a password or
// being suspended bumped the epoch, and there was no way to ask for it on its own.
// The case it is for is somebody who thinks a session is open on a machine they no
// longer have — which is not a reason to change a password they still trust.
//
// Revoking a device is a different thing and stays a different endpoint. This ends
// sessions; DELETE /v1/me/devices/{id} ends a device's ability to hold one at all.
func (s *Service) SignOutEverywhere(ctx context.Context, userID uuid.UUID) error {
	return s.repo.BumpSessionEpoch(ctx, userID)
}
