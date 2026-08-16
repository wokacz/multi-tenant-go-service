package v1

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/go-example/internal/api/problem"
	"github.com/wokacz/go-example/internal/domain/orgs"
	"github.com/wokacz/go-example/internal/store/models"
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
	UserID   uuid.UUID             `json:"user_id" format:"uuid" doc:"The account behind the membership"`
	Name     string                `json:"name" doc:"Display name"`
	Email    string                `json:"email" format:"email" doc:"Email address"`
	Status   string                `json:"status" enum:"invited,active,suspended" doc:"Whether the membership grants anything"`
	JoinedAt *time.Time            `json:"joined_at,omitempty" doc:"When the membership first became active"`
	Roles    []RoleSummaryResponse `json:"roles" doc:"Roles held in this organization"`
}

type RoleSummaryResponse struct {
	ID       uuid.UUID `json:"id" format:"uuid"`
	Key      string    `json:"key" doc:"Stable identifier, and the translation key"`
	Name     string    `json:"name" doc:"Display name, already localised"`
	IsSystem bool      `json:"is_system" doc:"Whether the role ships with the product and cannot be edited"`
}

func newMemberResponse(m *orgs.Member) MemberResponse {
	roles := make([]RoleSummaryResponse, 0, len(m.Roles))
	for _, role := range m.Roles {
		roles = append(roles, RoleSummaryResponse{
			ID: role.ID, Key: role.Key, Name: role.Name, IsSystem: role.IsSystem,
		})
	}

	return MemberResponse{
		ID:       m.ID,
		UserID:   m.UserID,
		Name:     m.Name,
		Email:    m.Email,
		Status:   string(m.Status),
		JoinedAt: m.JoinedAt,
		Roles:    roles,
	}
}

type ListMembersInput struct {
	OrgID uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
}

type ListMembersOutput struct {
	Body struct {
		Members []MemberResponse `json:"members"`
	}
}

type AddMemberRequest struct {
	Email   string      `json:"email" format:"email" maxLength:"255" doc:"Address of an existing account"`
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
}

func registerMembers(api huma.API, service *orgs.Service) {
	h := &memberHandlers{orgs: service}

	huma.Register(api, huma.Operation{
		OperationID: "list-members",
		Method:      http.MethodGet,
		Path:        Prefix + "/orgs/{orgID}/members",
		Summary:     "List members",
		Description: "Everyone in the organization, including invitations and " +
			"suspensions, with the roles each holds. Requires members.read.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   orgErrors(),
	}, h.list)

	huma.Register(api, huma.Operation{
		OperationID: "add-member",
		Method:      http.MethodPost,
		Path:        Prefix + "/orgs/{orgID}/members",
		Summary:     "Add a member",
		Description: "Puts an existing account into the organization with the given " +
			"roles. Requires members.invite, and every role named must be one the " +
			"caller could grant themselves — otherwise this endpoint would be a " +
			"way to acquire permissions the caller does not have.",
		Tags:          []string{"organizations"},
		Security:      bearer(),
		DefaultStatus: http.StatusCreated,
		Errors:        append(orgErrors(), http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests),
	}, h.add)

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
}

func (h *memberHandlers) list(ctx context.Context, _ *ListMembersInput) (*ListMembersOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	members, err := h.orgs.Members(ctx, grant)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	out := &ListMembersOutput{}
	out.Body.Members = make([]MemberResponse, 0, len(members))

	for i := range members {
		out.Body.Members = append(out.Body.Members, newMemberResponse(&members[i]))
	}

	return out, nil
}

func (h *memberHandlers) add(ctx context.Context, in *AddMemberInput) (*MemberOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	member, err := h.orgs.AddMember(ctx, grant, in.Body.Email, in.Body.RoleIDs)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &MemberOutput{Body: newMemberResponse(member)}, nil
}

func (h *memberHandlers) setStatus(ctx context.Context, in *UpdateMemberStatusInput) (*MemberOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	status := models.MembershipStatus(in.Body.Status)
	if err := h.orgs.SetMemberStatus(ctx, grant, in.MemberID, status); err != nil {
		return nil, problem.Error(ctx, err)
	}

	member, err := h.orgs.Member(ctx, grant, in.MemberID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &MemberOutput{Body: newMemberResponse(member)}, nil
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

	return &MemberOutput{Body: newMemberResponse(member)}, nil
}
