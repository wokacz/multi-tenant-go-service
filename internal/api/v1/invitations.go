package v1

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/telemetry"
)

// InvitationTokenRequest carries the secret from the message.
//
// It is a body rather than a path parameter, and that is deliberate: a token in a
// URL ends up in access logs, in browser history and in the Referer header of
// whatever the page loads next. The same reason password resets take their code in
// a body.
type InvitationTokenRequest struct {
	Token string `json:"token" minLength:"16" maxLength:"128" doc:"Token from the invitation message"`
}

type InvitationTokenInput struct {
	Body InvitationTokenRequest
}

// InvitationResponse is an offer as the invitee sees it. It carries no token: this
// list exists so an invitation is visible somewhere in the product, not so it can
// be accepted from here.
type InvitationResponse struct {
	Organization OrganizationResponse `json:"organization"`
	Email        string               `json:"email" format:"email" doc:"Address the invitation was issued to"`
	Roles        []string             `json:"roles" doc:"Role keys it will grant once accepted"`
	ExpiresAt    time.Time            `json:"expires_at" doc:"After this it can no longer be accepted"`
}

// InvitationOutput is what an administrator gets back after inviting somebody.
// No token: an administrator who could read it back could accept on the invitee's
// behalf, which is exactly what the token replaced.
type InvitationOutput struct {
	Body InvitationResponse
}

func newInvitationResponse(i *orgs.Invitation) InvitationResponse {
	return InvitationResponse{
		Organization: newOrganizationResponse(&i.Organization),
		Email:        i.Email,
		Roles:        i.RoleKeys,
		ExpiresAt:    i.ExpiresAt,
	}
}

type ListMyInvitationsInput struct{}

type ListMyInvitationsOutput struct {
	Body struct {
		Invitations []InvitationResponse `json:"invitations"`
	}
}

type invitationHandlers struct {
	orgs  *orgs.Service
	users *user.Service
	tel   *telemetry.Telemetry
}

func registerInvitations(api huma.API, service *orgs.Service, users *user.Service, tel *telemetry.Telemetry) {
	h := &invitationHandlers{orgs: service, users: users, tel: tel}

	huma.Register(api, huma.Operation{
		OperationID: "list-my-invitations",
		Method:      http.MethodGet,
		Path:        Prefix + "/me/invitations",
		Summary:     "List invitations addressed to this account",
		Description: "What has been offered to the caller's address, without the " +
			"tokens. Accepting still needs the token from the message — this is " +
			"here so somebody who deleted the mail can see that an offer is open " +
			"and ask for it again.",
		Tags:     []string{"users"},
		Security: bearer(),
		Errors:   []int{http.StatusUnauthorized, http.StatusNotFound},
	}, h.list)

	huma.Register(api, huma.Operation{
		OperationID: "accept-invitation",
		Method:      http.MethodPost,
		Path:        Prefix + "/me/invitations/accept",
		Summary:     "Accept an invitation",
		Description: "Takes up an offer using the token from the message, creating an " +
			"active membership with the roles the invitation carries. The roles " +
			"come from the invitation, never from this request. The address it was " +
			"issued to must match the caller's account: 409 says the token is good " +
			"but was meant for somebody else, and 410 that it has expired.",
		Tags:          []string{"users"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors: []int{
			http.StatusUnauthorized,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusGone,
		},
	}, h.accept)

	huma.Register(api, huma.Operation{
		OperationID: "decline-invitation",
		Method:      http.MethodPost,
		Path:        Prefix + "/me/invitations/decline",
		Summary:     "Decline an invitation",
		Description: "Removes the offer. Holding the token is the authorization: " +
			"whoever can read the mailbox is entitled to refuse on its behalf.",
		Tags:          []string{"users"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusGone},
	}, h.decline)
}

func (h *invitationHandlers) list(ctx context.Context, _ *ListMyInvitationsInput) (*ListMyInvitationsOutput, error) {
	email, err := h.accountEmail(ctx)
	if err != nil {
		return nil, err
	}

	invitations, err := h.orgs.MyInvitations(ctx, email)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	out := &ListMyInvitationsOutput{}
	out.Body.Invitations = make([]InvitationResponse, 0, len(invitations))

	for i := range invitations {
		out.Body.Invitations = append(out.Body.Invitations, InvitationResponse{
			Organization: newOrganizationResponse(&invitations[i].Organization),
			Email:        invitations[i].Email,
			Roles:        invitations[i].RoleKeys,
			ExpiresAt:    invitations[i].ExpiresAt,
		})
	}

	return out, nil
}

func (h *invitationHandlers) accept(ctx context.Context, in *InvitationTokenInput) (*struct{}, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	email, err := h.accountEmail(ctx)
	if err != nil {
		return nil, err
	}

	if err := h.orgs.AcceptInvitation(ctx, sess.UserID, email, in.Body.Token); err != nil {
		return nil, problem.Error(ctx, err)
	}

	h.tel.Metrics.CountInvitation(ctx, telemetry.EventInvitationAccepted)

	return nil, nil
}

func (h *invitationHandlers) decline(ctx context.Context, in *InvitationTokenInput) (*struct{}, error) {
	if _, ok := auth.SessionFrom(ctx); !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	if err := h.orgs.DeclineInvitation(ctx, in.Body.Token); err != nil {
		return nil, problem.Error(ctx, err)
	}

	h.tel.Metrics.CountInvitation(ctx, telemetry.EventInvitationDeclined)

	return nil, nil
}

// accountEmail reads the address from the account rather than from the request.
//
// Taking it from the body would let a caller name somebody else's address and
// accept their invitation with a token they had somehow got hold of; taking it
// from the token would remove the second condition entirely.
func (h *invitationHandlers) accountEmail(ctx context.Context) (string, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return "", problem.Error(ctx, user.ErrUnauthorized)
	}

	account, err := h.users.ByID(ctx, sess.UserID)
	if err != nil {
		return "", problem.Error(ctx, err)
	}

	return account.Email, nil
}
