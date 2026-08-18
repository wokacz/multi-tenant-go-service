package v1

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

// OrganizationResponse is the wire representation of an organization.
type OrganizationResponse struct {
	ID        uuid.UUID `json:"id" format:"uuid" doc:"Unique identifier"`
	Slug      string    `json:"slug" doc:"Stable human-readable handle"`
	Name      string    `json:"name" doc:"Display name"`
	CreatedAt time.Time `json:"created_at" doc:"When the organization was created"`
}

func newOrganizationResponse(o *ent.Organization) OrganizationResponse {
	return OrganizationResponse{
		ID:        o.ID,
		Slug:      o.Slug,
		Name:      o.Name,
		CreatedAt: o.CreatedAt,
	}
}

// MembershipResponse is one entry in the caller's own list of organizations.
//
// Invitations and suspensions are included rather than filtered out, with the
// status spelled out, so a client can show "you were suspended here" instead of
// having the organization silently vanish from the list.
//
// An invitation is not one of these. It is not a membership, and it has its own
// listing at GET /v1/me/invitations — being in both would be two answers to
// "which organizations am I in".
type MembershipResponse struct {
	ID           uuid.UUID            `json:"id" format:"uuid" doc:"Membership id, which is what DELETE /v1/me/memberships/{membershipID} takes to leave"`
	Organization OrganizationResponse `json:"organization"`
	Status       string               `json:"status" enum:"active,suspended" doc:"Where the caller stands in this organization"`
	Roles        []string             `json:"roles" doc:"Keys of the roles the caller holds here"`
}

func newMembershipResponse(m *orgs.Membership) MembershipResponse {
	roles := m.RoleKeys
	if roles == nil {
		// [] rather than null: a client mapping over the result should not have
		// to special-case a member who holds no roles yet.
		roles = []string{}
	}

	return MembershipResponse{
		ID:           m.ID,
		Organization: newOrganizationResponse(&m.Organization),
		Status:       string(m.Status),
		Roles:        roles,
	}
}

type ListMyOrganizationsInput struct{}

type ListMyOrganizationsOutput struct {
	Body struct {
		Organizations []MembershipResponse `json:"organizations"`
	}
}

// GetOrganizationInput declares {orgID} so huma documents the parameter and
// validates its shape.
//
// The handler deliberately does not read it. The organization the caller was
// authorized for lives on the context as part of the authz.Grant, and taking it
// from the request instead would let a handler act on a different organization
// than the one the middleware checked. TestHandlersDoNotReadTheOrgIDParameter
// enforces that this field stays unused.
type GetOrganizationInput struct {
	OrgID uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
}

type GetOrganizationOutput struct {
	Body OrganizationResponse
}

type organizationHandlers struct {
	orgs *orgs.Service
}

func registerOrganizations(api huma.API, service *orgs.Service) {
	h := &organizationHandlers{orgs: service}

	huma.Register(api, huma.Operation{
		OperationID: "list-my-organizations",
		Method:      http.MethodGet,
		Path:        Prefix + "/me/organizations",
		Summary:     "List the caller's organizations",
		Description: "Returns every organization the account belongs to, including " +
			"the ones it is suspended in, so a client can tell those apart. " +
			"Invitations are not memberships and are listed separately at " +
			"GET /v1/me/invitations. Self-service: no permission is required, and " +
			"none can take it away.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   []int{http.StatusUnauthorized},
	}, h.mine)

	huma.Register(api, huma.Operation{
		OperationID: "get-organization",
		Method:      http.MethodGet,
		Path:        Prefix + "/orgs/{orgID}",
		Summary:     "Read an organization",
		Description: "Requires organization.read in the organization named in the " +
			"path. A caller with no active membership gets 404 rather than 403: " +
			"whether an organization exists is not something a stranger may find " +
			"out by trying identifiers.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   orgErrors(),
	}, h.get)

	huma.Register(api, huma.Operation{
		OperationID: "update-organization",
		Method:      http.MethodPatch,
		Path:        Prefix + "/orgs/{orgID}",
		Summary:     "Rename an organization",
		Description: "Requires organization.update. The slug is not editable: it " +
			"appears in links people have already shared.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   append(orgErrors(), http.StatusUnprocessableEntity),
	}, h.update)

	huma.Register(api, huma.Operation{
		OperationID: "delete-organization",
		Method:      http.MethodDelete,
		Path:        Prefix + "/orgs/{orgID}",
		Summary:     "Delete an organization",
		Description: "Requires organization.delete. The default organization is " +
			"protected and is refused with 409 — an installation that lost its " +
			"only organization has no working accounts and no screen to undo it " +
			"from.",
		Tags:          []string{"organizations"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors:        append(orgErrors(), http.StatusConflict),
	}, h.remove)

	huma.Register(api, huma.Operation{
		OperationID: "leave-organization",
		Method:      http.MethodDelete,
		Path:        Prefix + "/me/memberships/{membershipID}",
		Summary:     "Leave an organization",
		Description: "Removes the caller's own membership. Self-service: no " +
			"permission is required, and none can take it away — until this " +
			"existed the only ways out were asking an administrator or calling " +
			"remove-member on yourself, which needs members.remove, so the callers " +
			"most likely to want out were the ones who could not. " +
			"The path names the membership rather than the organization, and the id " +
			"comes from GET /v1/me/organizations: an {orgID} in a path means the " +
			"middleware resolved a permission there, and nothing does here. " +
			"A membership that is not the caller's own is 404, and the last owner of " +
			"an organization is refused with 409 — appoint another owner first.",
		Tags:          []string{"organizations"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors: []int{
			http.StatusUnauthorized,
			http.StatusNotFound,
			http.StatusConflict,
		},
	}, h.leave)
}

// leave is self-service, so there is no grant to read: the session says who is
// asking, and orgs.Leave resolves the membership inside the caller's own list.
func (h *organizationHandlers) leave(ctx context.Context, in *LeaveOrganizationInput) (*struct{}, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	if err := h.orgs.Leave(ctx, sess.UserID, in.MembershipID); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}

type LeaveOrganizationInput struct {
	MembershipID uuid.UUID `path:"membershipID" format:"uuid" doc:"The caller's own membership, from GET /v1/me/organizations"`
}

type UpdateOrganizationRequest struct {
	Name string `json:"name" minLength:"1" maxLength:"100" doc:"Display name"`
}

type UpdateOrganizationInput struct {
	OrgID uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	Body  UpdateOrganizationRequest
}

func (h *organizationHandlers) update(ctx context.Context, in *UpdateOrganizationInput) (*GetOrganizationOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	org, err := h.orgs.Rename(ctx, grant, in.Body.Name)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &GetOrganizationOutput{Body: newOrganizationResponse(org)}, nil
}

func (h *organizationHandlers) remove(ctx context.Context, _ *GetOrganizationInput) (*struct{}, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	if err := h.orgs.Delete(ctx, grant); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}

func (h *organizationHandlers) mine(ctx context.Context, _ *ListMyOrganizationsInput) (*ListMyOrganizationsOutput, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	memberships, err := h.orgs.Mine(ctx, sess.UserID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	out := &ListMyOrganizationsOutput{}
	out.Body.Organizations = make([]MembershipResponse, 0, len(memberships))

	for i := range memberships {
		out.Body.Organizations = append(out.Body.Organizations, newMembershipResponse(&memberships[i]))
	}

	return out, nil
}

func (h *organizationHandlers) get(ctx context.Context, _ *GetOrganizationInput) (*GetOrganizationOutput, error) {
	grant, ok := authz.GrantFrom(ctx)
	if !ok {
		// Only reachable if the middleware chain was rewired. Refusing beats
		// falling back to the request, which is the exact substitution the
		// grant exists to prevent.
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	org, err := h.orgs.Organization(ctx, grant.OrganizationID())
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &GetOrganizationOutput{Body: newOrganizationResponse(org)}, nil
}
