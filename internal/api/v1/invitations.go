package v1

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
)

type InvitationPathInput struct {
	InvitationID uuid.UUID `path:"invitationID" format:"uuid" doc:"Membership id of the outstanding invitation"`
}

type invitationHandlers struct {
	orgs  *orgs.Service
	users *user.Service
}

func registerInvitations(api huma.API, service *orgs.Service, users *user.Service) {
	h := &invitationHandlers{orgs: service, users: users}

	huma.Register(api, huma.Operation{
		OperationID: "accept-invitation",
		Method:      http.MethodPost,
		Path:        Prefix + "/me/invitations/{invitationID}/accept",
		Summary:     "Accept an invitation",
		Description: "Turns an outstanding invitation addressed to this account " +
			"into an active membership. Self-service: no permission is required, " +
			"and an invitation for another address is indistinguishable from a " +
			"missing one.",
		Tags:          []string{"users"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict},
	}, h.accept)

	huma.Register(api, huma.Operation{
		OperationID: "decline-invitation",
		Method:      http.MethodDelete,
		Path:        Prefix + "/me/invitations/{invitationID}",
		Summary:     "Decline an invitation",
		Description: "Withdraws an outstanding invitation addressed to this account. " +
			"Self-service: an invitation for another address is indistinguishable " +
			"from a missing one.",
		Tags:          []string{"users"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusNotFound},
	}, h.decline)
}

func (h *invitationHandlers) accept(ctx context.Context, in *InvitationPathInput) (*struct{}, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	account, err := h.users.ByID(ctx, sess.UserID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	if err := h.orgs.AcceptInvitation(ctx, sess.UserID, account.Email, in.InvitationID); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}

func (h *invitationHandlers) decline(ctx context.Context, in *InvitationPathInput) (*struct{}, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	account, err := h.users.ByID(ctx, sess.UserID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	if err := h.orgs.DeclineInvitation(ctx, account.Email, in.InvitationID); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}
