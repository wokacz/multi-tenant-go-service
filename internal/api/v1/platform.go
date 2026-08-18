package v1

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// The installation-wide surface.
//
// These are the only operations measured against the whole installation rather
// than one organization, and the only ones whose paths carry no {orgID}. The
// distinction is enforced from both ends: authz.ScopeSystem on the rule, and
// TestOrgScopedRulesLiveOnOrgScopedPaths refusing a system-scoped rule on a
// path that names an organization.
//
// The path parameter is deliberately {id} and not {orgID}. The middleware reads
// {orgID} to resolve an organization to authorize *in*; here the organization is
// the object being acted on, not the scope the caller was granted, and reusing
// the name would make the two look interchangeable.

type PlatformOrganizationResponse struct {
	OrganizationResponse

	IsProtected bool `json:"is_protected" doc:"Refuses deletion. The default organization carries it."`

	// Owners is only on this response, not on the organization-scoped one: how many
	// owners an installation's other tenants have is not a member's business.
	Owners int `json:"owners" doc:"How many people can administer it, counted the same way the last-owner rule counts. Zero means nobody can, and POST /v1/platform/organizations/{id}/owners is the way back."`
}

// PlatformUserResponse is an account as an installation administrator sees it.
//
// It is a different type from UserResponse on purpose: this one carries the
// suspension, which is nobody's business but an administrator's, and keeping
// them separate stops a field added here from leaking into /v1/me.
type PlatformUserResponse struct {
	ID          uuid.UUID `json:"id" format:"uuid"`
	Name        string    `json:"name"`
	Email       string    `json:"email" format:"email"`
	Suspended   bool      `json:"suspended" doc:"Blocked from signing in anywhere"`
	Locale      string    `json:"locale,omitempty"`
	CreatedAt   string    `json:"created_at"`
	SuspendedAt *string   `json:"suspended_at,omitempty"`
}

type PageInput struct {
	Limit  int `query:"limit" minimum:"1" maximum:"100" default:"100" doc:"How many to return"`
	Offset int `query:"offset" minimum:"0" default:"0" doc:"How many to skip"`
}

// ListPlatformOrganizationsInput is the page plus the one filter that answers a
// question an administrator cannot answer any other way.
type ListPlatformOrganizationsInput struct {
	PageInput

	WithoutOwner bool `query:"without_owner" doc:"Only organizations nobody can administer"`
}

type ListPlatformOrganizationsOutput struct {
	Body struct {
		Organizations []PlatformOrganizationResponse `json:"organizations"`
	}
}

type CreatePlatformOrganizationRequest struct {
	Slug string `json:"slug" minLength:"1" maxLength:"63" doc:"Lowercase handle, unique in the installation"`
	Name string `json:"name" minLength:"1" maxLength:"100" doc:"Display name"`
}

type CreatePlatformOrganizationInput struct {
	Body CreatePlatformOrganizationRequest
}

type PlatformOrganizationOutput struct {
	Body PlatformOrganizationResponse
}

type PlatformOrganizationPathInput struct {
	ID uuid.UUID `path:"id" format:"uuid" doc:"Organization id"`
}

type ListPlatformUsersOutput struct {
	Body struct {
		Users []PlatformUserResponse `json:"users"`
	}
}

type PlatformUserPathInput struct {
	ID uuid.UUID `path:"id" format:"uuid" doc:"Account id"`
}

type SuspendPlatformUserRequest struct {
	Suspended bool `json:"suspended" doc:"true blocks the account everywhere, false restores it"`
}

type SuspendPlatformUserInput struct {
	ID   uuid.UUID `path:"id" format:"uuid" doc:"Account id"`
	Body SuspendPlatformUserRequest
}

type platformHandlers struct {
	orgs  *orgs.Service
	users *user.Service
}

// platformErrors are the statuses every installation-wide operation can produce
// before its handler runs. There is no 404-instead-of-403 here: a caller who
// reaches these has already been established as an installation administrator,
// so there is nothing left to hide from them.
func platformErrors() []int {
	return []int{http.StatusUnauthorized, http.StatusForbidden}
}

func registerPlatform(api huma.API, service *orgs.Service, users *user.Service) {
	h := &platformHandlers{orgs: service, users: users}

	huma.Register(api, huma.Operation{
		OperationID: "list-platform-organizations",
		Method:      http.MethodGet,
		Path:        Prefix + "/platform/organizations",
		Summary:     "List every organization",
		Description: "Requires platform.organizations.read. The only listing that " +
			"crosses tenants, which is why it is measured against the " +
			"installation rather than any one organization.\n\n" +
			"Each row says how many owners the organization has, counted exactly the " +
			"way the last-owner rule counts them: an active membership holding owner " +
			"whose account still exists. without_owner=true keeps only the ones at " +
			"zero — an organization gets there when its last owner's account is " +
			"deleted, and appointing a new one was already possible while finding " +
			"such an organization was not.",
		Tags:     []string{"platform"},
		Security: bearer(),
		Errors:   platformErrors(),
	}, h.listOrganizations)

	huma.Register(api, huma.Operation{
		OperationID: "create-platform-organization",
		Method:      http.MethodPost,
		Path:        Prefix + "/platform/organizations",
		Summary:     "Create an organization",
		Description: "Requires platform.organizations.create. The organization is " +
			"created with the roles that ship with the product, and with nobody " +
			"in it — the creator is not silently made an owner, because that is a " +
			"separate and visible act.",
		Tags:          []string{"platform"},
		Security:      bearer(),
		DefaultStatus: http.StatusCreated,
		Errors:        append(platformErrors(), http.StatusConflict, http.StatusUnprocessableEntity),
	}, h.createOrganization)

	huma.Register(api, huma.Operation{
		OperationID: "delete-platform-organization",
		Method:      http.MethodDelete,
		Path:        Prefix + "/platform/organizations/{id}",
		Summary:     "Delete any organization",
		Description: "Requires platform.organizations.delete. The default " +
			"organization is protected and is refused with 409.",
		Tags:          []string{"platform"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors:        append(platformErrors(), http.StatusNotFound, http.StatusConflict),
	}, h.deleteOrganization)

	huma.Register(api, huma.Operation{
		OperationID: "list-platform-users",
		Method:      http.MethodGet,
		Path:        Prefix + "/platform/users",
		Summary:     "List every account",
		Description: "Requires platform.users.read.",
		Tags:        []string{"platform"},
		Security:    bearer(),
		Errors:      platformErrors(),
	}, h.listUsers)

	huma.Register(api, huma.Operation{
		OperationID: "suspend-platform-user",
		Method:      http.MethodPatch,
		Path:        Prefix + "/platform/users/{id}",
		Summary:     "Suspend or restore an account",
		Description: "Requires platform.users.suspend. Suspending takes effect on " +
			"tokens already issued, on the next request rather than at expiry — " +
			"the same promise device revocation makes. Memberships and roles are " +
			"kept, so restoring gives back exactly what was there.",
		Tags:     []string{"platform"},
		Security: bearer(),
		Errors:   append(platformErrors(), http.StatusNotFound),
	}, h.suspendUser)

	huma.Register(api, huma.Operation{
		OperationID: "delete-platform-user",
		Method:      http.MethodDelete,
		Path:        Prefix + "/platform/users/{id}",
		Summary:     "Delete an account",
		Description: "Requires platform.users.delete. A soft delete: the account " +
			"stops working everywhere and its devices are revoked, but the row " +
			"survives so login history and audit records keep making sense.",
		Tags:          []string{"platform"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors:        append(platformErrors(), http.StatusNotFound, http.StatusConflict),
	}, h.deleteUser)

	huma.Register(api, huma.Operation{
		OperationID: "appoint-organization-owner",
		Method:      http.MethodPost,
		Path:        Prefix + "/platform/organizations/{id}/owners",
		Summary:     "Appoint an organization's owner",
		Description: "Makes an account the owner of any organization, without the caller " +
			"joining it. Requires platform.organizations.owners.assign.\n\n" +
			"This is the other half of creating an organization. Creating one " +
			"deliberately leaves it empty, and adding a member needs a permission " +
			"inside it — which a platform administrator does not have — so without " +
			"this an organization made here had nobody in it and no way to get " +
			"anybody. It is also the way back from an organization that lost its " +
			"last owner.\n\n" +
			"It does not grant the installation-wide role: owning one organization " +
			"and administering the installation are separate authorizations with " +
			"separate endpoints.",
		Tags:          []string{"platform"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors:        platformErrors(),
	}, h.appointOwner)
}

func newPlatformOrganizationResponse(o *orgs.OrganizationSummary) PlatformOrganizationResponse {
	return PlatformOrganizationResponse{
		OrganizationResponse: newOrganizationResponse(&o.Organization),
		IsProtected:          o.IsProtected,
		Owners:               o.Owners,
	}
}

func newPlatformUserResponse(u *models.User) PlatformUserResponse {
	out := PlatformUserResponse{
		ID:        u.ID,
		Name:      u.Name,
		Email:     u.Email,
		Suspended: u.IsSuspended(),
		Locale:    u.Locale,
		CreatedAt: u.CreatedAt.Format(time.RFC3339),
	}

	if u.SuspendedAt != nil {
		at := u.SuspendedAt.Format(time.RFC3339)
		out.SuspendedAt = &at
	}

	return out
}

func (h *platformHandlers) listOrganizations(
	ctx context.Context,
	in *ListPlatformOrganizationsInput,
) (*ListPlatformOrganizationsOutput, error) {
	filter := orgs.OrganizationFilter{WithoutOwner: in.WithoutOwner}

	list, err := h.orgs.AllOrganizations(ctx, filter, in.Limit, in.Offset)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	out := &ListPlatformOrganizationsOutput{}
	out.Body.Organizations = make([]PlatformOrganizationResponse, 0, len(list))

	for i := range list {
		out.Body.Organizations = append(out.Body.Organizations, newPlatformOrganizationResponse(&list[i]))
	}

	return out, nil
}

func (h *platformHandlers) createOrganization(
	ctx context.Context,
	in *CreatePlatformOrganizationInput,
) (*PlatformOrganizationOutput, error) {
	org, err := h.orgs.CreateOrganization(ctx, in.Body.Slug, in.Body.Name)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	// Zero owners, and that is not a placeholder: creating an organization
	// deliberately leaves it empty, so the count is telling the truth and pointing
	// at the appoint-owner endpoint.
	summary := &orgs.OrganizationSummary{Organization: *org}

	return &PlatformOrganizationOutput{Body: newPlatformOrganizationResponse(summary)}, nil
}

func (h *platformHandlers) deleteOrganization(ctx context.Context, in *PlatformOrganizationPathInput) (*struct{}, error) {
	if err := h.orgs.DeleteOrganizationByID(ctx, in.ID); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}

func (h *platformHandlers) listUsers(ctx context.Context, in *PageInput) (*ListPlatformUsersOutput, error) {
	list, err := h.users.All(ctx, in.Limit, in.Offset)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	out := &ListPlatformUsersOutput{}
	out.Body.Users = make([]PlatformUserResponse, 0, len(list))

	for i := range list {
		out.Body.Users = append(out.Body.Users, newPlatformUserResponse(&list[i]))
	}

	return out, nil
}

func (h *platformHandlers) suspendUser(ctx context.Context, in *SuspendPlatformUserInput) (*struct{}, error) {
	// Suspending yourself would take away the permission needed to undo it, and
	// the account that could fix it is the one that is now blocked.
	if sess, ok := auth.SessionFrom(ctx); ok && sess.UserID == in.ID && in.Body.Suspended {
		return nil, problem.Error(ctx, user.ErrCannotSuspendSelf)
	}

	if err := h.users.Suspend(ctx, in.ID, in.Body.Suspended); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}

func (h *platformHandlers) deleteUser(ctx context.Context, in *PlatformUserPathInput) (*struct{}, error) {
	if sess, ok := auth.SessionFrom(ctx); ok && sess.UserID == in.ID {
		return nil, problem.Error(ctx, user.ErrCannotDeleteSelf)
	}

	if err := h.users.Delete(ctx, in.ID); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}

// AppointOwnerRequest names the account to put in charge.
type AppointOwnerRequest struct {
	UserID uuid.UUID `json:"user_id" format:"uuid" doc:"Account to make owner"`
}

type AppointOwnerInput struct {
	ID   uuid.UUID `path:"id" format:"uuid" doc:"Organization id"`
	Body AppointOwnerRequest
}

func (h *platformHandlers) appointOwner(ctx context.Context, in *AppointOwnerInput) (*struct{}, error) {
	if err := h.orgs.AppointOwner(ctx, in.ID, in.Body.UserID); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}
