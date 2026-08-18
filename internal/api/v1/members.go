package v1

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/i18n"
	"github.com/wokacz/multi-tenant-go-service/internal/mail"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/telemetry"
)

// MemberResponse is one person's place in an organization.
//
// It carries the membership id rather than the account id as its primary
// identifier, and every member endpoint addresses that. A membership is
// organization-scoped, so an id from another tenant simply does not resolve;
// addressing people by account id would make every handler responsible for
// checking that the account is actually in this organization.
type MemberResponse struct {
	ID       uuid.UUID             `json:"id" format:"uuid" doc:"Membership id, used to address this person here"`
	UserID   *uuid.UUID            `json:"user_id,omitempty" format:"uuid" doc:"The account behind the membership. Always present now that an invitation is not a membership; the field stays optional so clients written against the older contract keep working."`
	Name     string                `json:"name,omitempty" doc:"Display name"`
	Email    string                `json:"email" format:"email" doc:"Email address"`
	Status   string                `json:"status" enum:"active,suspended" doc:"Whether the membership grants anything"`
	JoinedAt *time.Time            `json:"joined_at,omitempty" doc:"When the membership first became active"`
	Roles    []RoleSummaryResponse `json:"roles" doc:"Roles held in this organization"`
}

type RoleSummaryResponse struct {
	ID       uuid.UUID `json:"id" format:"uuid"`
	Key      string    `json:"key" doc:"Stable identifier, and the translation key"`
	Name     string    `json:"name" doc:"Display name. A shipped role is named in the caller language; a role the organization created is shown as it was typed."`
	IsSystem bool      `json:"is_system" doc:"Whether the role ships with the product and cannot be edited"`
}

func newMemberResponse(locale i18n.Locale, m *orgs.Member) MemberResponse {
	roles := make([]RoleSummaryResponse, 0, len(m.Roles))

	for _, role := range m.Roles {
		name, _ := roleLabel(locale, role.Key, role.Name, "", role.IsSystem)
		roles = append(roles, RoleSummaryResponse{
			ID: role.ID, Key: role.Key, Name: name, IsSystem: role.IsSystem,
		})
	}

	// No account to hide any more: every member is a person. The pointer stays
	// because the field is optional in the contract, and changing that would break
	// clients for no gain.
	id := m.UserID

	return MemberResponse{
		ID:       m.ID,
		UserID:   &id,
		Name:     m.Name,
		Email:    m.Email,
		Status:   string(m.Status),
		JoinedAt: m.JoinedAt,
		Roles:    roles,
	}
}

type ListMembersInput struct {
	OrgID uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	PageInput
}

type ListMembersOutput struct {
	Body struct {
		Members []MemberResponse `json:"members"`
	}
}

type AddMemberRequest struct {
	Email   string      `json:"email" format:"email" maxLength:"255" doc:"Address to invite. An existing account is not required."`
	RoleIDs []uuid.UUID `json:"role_ids" doc:"Roles to grant. Every one must be a role the caller could grant themselves."`
}

type AddMemberInput struct {
	OrgID uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	Body  AddMemberRequest
}

type MemberOutput struct {
	Body MemberResponse
}

type UpdateMemberStatusRequest struct {
	Status string `json:"status" enum:"active,suspended" doc:"Suspending withdraws every permission without losing the role assignments"`
}

type UpdateMemberStatusInput struct {
	OrgID    uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	MemberID uuid.UUID `path:"memberID" format:"uuid" doc:"Membership id"`
	Body     UpdateMemberStatusRequest
}

type MemberPathInput struct {
	OrgID    uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	MemberID uuid.UUID `path:"memberID" format:"uuid" doc:"Membership id"`
}

type SetMemberRolesRequest struct {
	RoleIDs []uuid.UUID `json:"role_ids" doc:"The complete set of roles this person should hold"`
}

type SetMemberRolesInput struct {
	OrgID    uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	MemberID uuid.UUID `path:"memberID" format:"uuid" doc:"Membership id"`
	Body     SetMemberRolesRequest
}

type memberHandlers struct {
	orgs *orgs.Service
	mail mail.Sender
	log  *slog.Logger
	tel  *telemetry.Telemetry
}

func registerMembers(
	api huma.API,
	service *orgs.Service,
	mailer mail.Sender,
	log *slog.Logger,
	tel *telemetry.Telemetry,
) {
	h := &memberHandlers{orgs: service, mail: mailer, log: log, tel: tel}

	huma.Register(api, huma.Operation{
		OperationID: "list-members",
		Method:      http.MethodGet,
		Path:        Prefix + "/orgs/{orgID}/members",
		Summary:     "List members",
		Description: "Everyone in the organization, suspensions included, with the " +
			"roles each holds. Invitations are not members and are listed at " +
			"GET /v1/orgs/{orgID}/invitations. Requires members.read.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   orgErrors(),
	}, h.list)

	huma.Register(api, huma.Operation{
		OperationID: "add-member",
		Method:      http.MethodPost,
		Path:        Prefix + "/orgs/{orgID}/members",
		Summary:     "Invite a member",
		Description: "Stores an invitation for the address with the given roles. " +
			"The account need not exist yet; unknown and known addresses produce " +
			"the same response, so the call cannot be used to discover who is " +
			"registered. Requires members.invite, and every role named must be " +
			"one the caller could grant themselves — otherwise this endpoint " +
			"would be a way to acquire permissions the caller does not have.",
		Tags:          []string{"organizations"},
		Security:      bearer(),
		DefaultStatus: http.StatusCreated,
		Errors:        append(orgErrors(), http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests),
	}, h.add)

	huma.Register(api, huma.Operation{
		OperationID: "invite-members",
		Method:      http.MethodPost,
		Path:        Prefix + "/orgs/{orgID}/invitations",
		Summary:     "Invite several members at once",
		Description: "One request, one role set, up to 50 addresses. It exists " +
			"because onboarding a team one request at a time ran into the rate " +
			"limit, and because each of those requests was identical apart from the " +
			"address. Requires members.invite, and every role named must be one the " +
			"caller could grant themselves. " +
			"Every address gets its own outcome rather than the batch failing on the " +
			"first refusal: invited, or already_member for somebody who is in the " +
			"organization or has an offer outstanding. An address that is not " +
			"registered anywhere is still invited, so this cannot be used to " +
			"discover who has an account. Match the results by address, not by " +
			"position.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   append(orgErrors(), http.StatusUnprocessableEntity, http.StatusTooManyRequests),
	}, h.inviteMany)

	huma.Register(api, huma.Operation{
		OperationID: "update-member-status",
		Method:      http.MethodPatch,
		Path:        Prefix + "/orgs/{orgID}/members/{memberID}",
		Summary:     "Suspend or reinstate a member",
		Description: "Suspending withdraws every permission immediately, on tokens " +
			"already issued, while keeping the role assignments so reinstating " +
			"restores exactly what they had. Requires members.suspend. Suspending " +
			"the last owner is refused with 409.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   append(orgErrors(), http.StatusConflict, http.StatusUnprocessableEntity),
	}, h.setStatus)

	huma.Register(api, huma.Operation{
		OperationID: "remove-member",
		Method:      http.MethodDelete,
		Path:        Prefix + "/orgs/{orgID}/members/{memberID}",
		Summary:     "Remove a member",
		Description: "Deletes the membership and its role assignments. Requires " +
			"members.remove. Removing the last owner is refused with 409.",
		Tags:          []string{"organizations"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors:        append(orgErrors(), http.StatusConflict),
	}, h.remove)

	huma.Register(api, huma.Operation{
		OperationID: "set-member-roles",
		Method:      http.MethodPut,
		Path:        Prefix + "/orgs/{orgID}/members/{memberID}/roles",
		Summary:     "Replace a member's roles",
		Description: "Sets the roles to exactly the list given. A replace rather than " +
			"an add and a remove, so two administrators editing the same person " +
			"concurrently cannot merge into a set neither of them chose. Requires " +
			"members.roles.assign; every role must be one the caller could grant " +
			"themselves; demoting the last owner is refused with 409.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   append(orgErrors(), http.StatusConflict, http.StatusUnprocessableEntity),
	}, h.setRoles)

	huma.Register(api, huma.Operation{
		OperationID: "list-invitations",
		Method:      http.MethodGet,
		Path:        Prefix + "/orgs/{orgID}/invitations",
		Summary:     "List outstanding invitations",
		Description: "Offers this organization has made that nobody has taken up yet. " +
			"Requires members.read. No tokens: the organization issued them but " +
			"cannot accept on anybody's behalf.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   orgErrors(),
	}, h.listInvitations)

	huma.Register(api, huma.Operation{
		OperationID: "reissue-invitation",
		Method:      http.MethodPost,
		Path:        Prefix + "/orgs/{orgID}/invitations/{invitationID}/reissue",
		Summary:     "Send an invitation again",
		Description: "Mails a fresh token and pushes the expiry out. The previous token " +
			"stops working — resending the same secret would keep a leaked link " +
			"alive, and issuing a second invitation would collide with the first. " +
			"Requires members.invite.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   append(orgErrors(), http.StatusTooManyRequests),
	}, h.reissueInvitation)

	huma.Register(api, huma.Operation{
		OperationID: "withdraw-invitation",
		Method:      http.MethodDelete,
		Path:        Prefix + "/orgs/{orgID}/invitations/{invitationID}",
		Summary:     "Withdraw an invitation",
		Description: "Takes back an offer. Requires members.invite — the same " +
			"permission as sending one, because this is the third step of that " +
			"lifecycle rather than a way of taking somebody's access away: nobody " +
			"has any yet.\n\n" +
			"Distinct from the invitee declining it: the two are authorized by " +
			"different facts — the invitee holds the token, the organization holds " +
			"the permission — and the audit entry says which of them ended it.",
		Tags:          []string{"organizations"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors:        orgErrors(),
	}, h.withdrawInvitation)
}

func (h *memberHandlers) list(ctx context.Context, in *ListMembersInput) (*ListMembersOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	// The organization comes from the grant, never from in.OrgID. Only the page
	// comes off the request, and the service clamps it as well: the schema's maximum
	// refuses an oversized limit here, and the cap means the domain does not depend
	// on this layer having done so.
	members, err := h.orgs.Members(ctx, grant, in.Limit, in.Offset)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	locale := i18n.LocaleFrom(ctx)

	out := &ListMembersOutput{}
	out.Body.Members = make([]MemberResponse, 0, len(members))

	for i := range members {
		out.Body.Members = append(out.Body.Members, newMemberResponse(locale, &members[i]))
	}

	return out, nil
}

func (h *memberHandlers) add(ctx context.Context, in *AddMemberInput) (*InvitationOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	invitation, token, err := h.orgs.Invite(ctx, grant, in.Body.Email, in.Body.RoleIDs)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	h.tel.Metrics.CountInvitation(ctx, telemetry.EventInvitationSent)

	// The token exists only here and in the message. It is deliberately absent
	// from the response: an administrator who could read it back could accept on
	// the invitee's behalf, which is the whole thing the token replaced.
	if h.mail != nil {
		if err := h.mail.SendInvitation(ctx, invitation.Email,
			invitation.Organization.Name, token, invitation.ExpiresAt); err != nil {
			logger(h.log).ErrorContext(ctx, "invitation mail failed", "error", err)
		}
	}

	return &InvitationOutput{Body: newInvitationResponse(invitation)}, nil
}

// InviteMembersRequest is one role set and a list of addresses.
//
// The schema does the counting: minItems, maxItems and uniqueItems mean an empty
// list, an over-long one and a repeated address are refused at the edge with a
// message naming the field. The service repeats the checks because it has to
// normalise first — Ada@example.com and ada@example.com are one address, and
// uniqueItems cannot see that.
type InviteMembersRequest struct {
	Emails  []string    `json:"emails" minItems:"1" maxItems:"50" uniqueItems:"true" format:"email" doc:"Addresses to invite. An existing account is not required."`
	RoleIDs []uuid.UUID `json:"role_ids" doc:"Roles to grant every one of them. Each must be a role the caller could grant themselves."`
}

type InviteMembersInput struct {
	OrgID uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	Body  InviteMembersRequest
}

// InviteResultResponse is one address's answer.
type InviteResultResponse struct {
	Email  string `json:"email" format:"email" doc:"The address, normalised"`
	Status string `json:"status" enum:"invited,already_member" doc:"What happened to this one"`
}

type InviteMembersOutput struct {
	Body struct {
		Results []InviteResultResponse `json:"results"`
	}
}

func (h *memberHandlers) inviteMany(ctx context.Context, in *InviteMembersInput) (*InviteMembersOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	outcomes, err := h.orgs.InviteMany(ctx, grant, in.Body.Emails, in.Body.RoleIDs)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	out := &InviteMembersOutput{}
	out.Body.Results = make([]InviteResultResponse, 0, len(outcomes))

	for i := range outcomes {
		outcome := &outcomes[i]

		status := "already_member"
		if outcome.Invitation != nil {
			status = "invited"

			h.tel.Metrics.CountInvitation(ctx, telemetry.EventInvitationSent)

			// One message per invitation, each with its own token. A failure to
			// send is logged and does not fail the request: the row exists, and
			// the invitation can be reissued.
			if h.mail != nil {
				if err := h.mail.SendInvitation(ctx, outcome.Invitation.Email,
					outcome.Invitation.Organization.Name, outcome.Token,
					outcome.Invitation.ExpiresAt); err != nil {
					logger(h.log).ErrorContext(ctx, "invitation mail failed", "error", err)
				}
			}
		}

		out.Body.Results = append(out.Body.Results, InviteResultResponse{
			Email: outcome.Email, Status: status,
		})
	}

	return out, nil
}

func (h *memberHandlers) setStatus(ctx context.Context, in *UpdateMemberStatusInput) (*MemberOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	status := ent.MembershipStatus(in.Body.Status)
	if err := h.orgs.SetMemberStatus(ctx, grant, in.MemberID, status); err != nil {
		return nil, problem.Error(ctx, err)
	}

	member, err := h.orgs.Member(ctx, grant, in.MemberID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &MemberOutput{Body: newMemberResponse(i18n.LocaleFrom(ctx), member)}, nil
}

func (h *memberHandlers) remove(ctx context.Context, in *MemberPathInput) (*struct{}, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	if err := h.orgs.RemoveMember(ctx, grant, in.MemberID); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}

func (h *memberHandlers) setRoles(ctx context.Context, in *SetMemberRolesInput) (*MemberOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	member, err := h.orgs.SetMemberRoles(ctx, grant, in.MemberID, in.Body.RoleIDs)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &MemberOutput{Body: newMemberResponse(i18n.LocaleFrom(ctx), member)}, nil
}

type InvitationPathInput struct {
	OrgID        uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	InvitationID uuid.UUID `path:"invitationID" format:"uuid" doc:"Invitation id"`
}

type ListInvitationsInput struct {
	OrgID uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
}

type ListInvitationsOutput struct {
	Body struct {
		Invitations []InvitationResponse `json:"invitations"`
	}
}

func (h *memberHandlers) listInvitations(ctx context.Context, _ *ListInvitationsInput) (*ListInvitationsOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	invitations, err := h.orgs.Invitations(ctx, grant)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	out := &ListInvitationsOutput{}
	out.Body.Invitations = make([]InvitationResponse, 0, len(invitations))

	for i := range invitations {
		out.Body.Invitations = append(out.Body.Invitations, newInvitationResponse(&invitations[i]))
	}

	return out, nil
}

func (h *memberHandlers) reissueInvitation(ctx context.Context, in *InvitationPathInput) (*InvitationOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	invitation, token, err := h.orgs.Reissue(ctx, grant, in.InvitationID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	h.tel.Metrics.CountInvitation(ctx, telemetry.EventInvitationReissued)

	if h.mail != nil {
		if err := h.mail.SendInvitation(ctx, invitation.Email,
			invitation.Organization.Name, token, invitation.ExpiresAt); err != nil {
			logger(h.log).ErrorContext(ctx, "invitation mail failed", "error", err)
		}
	}

	return &InvitationOutput{Body: newInvitationResponse(invitation)}, nil
}

func (h *memberHandlers) withdrawInvitation(ctx context.Context, in *InvitationPathInput) (*struct{}, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	if err := h.orgs.WithdrawInvitation(ctx, grant, in.InvitationID); err != nil {
		return nil, problem.Error(ctx, err)
	}

	h.tel.Metrics.CountInvitation(ctx, telemetry.EventInvitationWithdrawn)

	return nil, nil
}
