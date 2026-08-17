package v1

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
)

// RoleResponse is a role with everything it grants.
//
// Permissions are reported exactly as stored, including keys this build no
// longer defines. That is deliberate: a stale key grants nothing, but the
// settings screen has to be able to see it in order to remove it, and a
// response that silently filtered it would make the row unreachable.
type RoleResponse struct {
	ID          uuid.UUID `json:"id" format:"uuid"`
	Key         string    `json:"key" doc:"Stable identifier, and the translation key"`
	Name        string    `json:"name" doc:"Display name"`
	Description string    `json:"description,omitempty"`
	IsSystem    bool      `json:"is_system" doc:"Ships with the product: visible and clonable, never editable"`
	Permissions []string  `json:"permissions" doc:"Permission keys this role grants"`
	Members     int       `json:"members" doc:"How many people hold it"`
}

func newRoleResponse(r *orgs.Role) RoleResponse {
	permissions := make([]string, 0, len(r.Permissions))
	for _, perm := range r.Permissions {
		permissions = append(permissions, string(perm))
	}

	return RoleResponse{
		ID:          r.ID,
		Key:         r.Key,
		Name:        r.Name,
		Description: r.Description,
		IsSystem:    r.IsSystem,
		Permissions: permissions,
		Members:     r.Members,
	}
}

// PermissionResponse is one catalog entry, for the screen that edits a role.
type PermissionResponse struct {
	Key   string `json:"key" doc:"Stable identifier used everywhere else in the API"`
	Scope string `json:"scope" enum:"organization,system" doc:"What the permission is measured against"`
	Group string `json:"group" doc:"Heading to file it under"`
}

type ListRolesInput struct {
	OrgID uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	PageInput
}

type ListRolesOutput struct {
	Body struct {
		Roles []RoleResponse `json:"roles"`
	}
}

type RolePathInput struct {
	OrgID  uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	RoleID uuid.UUID `path:"roleID" format:"uuid" doc:"Role id"`
}

type RoleOutput struct {
	Body RoleResponse
}

type CreateRoleRequest struct {
	Key         string   `json:"key" minLength:"1" maxLength:"64" doc:"Lowercase identifier, unique in the organization"`
	Name        string   `json:"name" minLength:"1" maxLength:"100" doc:"Display name"`
	Description string   `json:"description,omitempty" maxLength:"255"`
	Permissions []string `json:"permissions" doc:"Permission keys. Every one must be a permission the caller holds."`
}

type CreateRoleInput struct {
	OrgID uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	Body  CreateRoleRequest
}

type UpdateRoleRequest struct {
	Name        string `json:"name" minLength:"1" maxLength:"100" doc:"Display name"`
	Description string `json:"description,omitempty" maxLength:"255"`
}

type UpdateRoleInput struct {
	OrgID  uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	RoleID uuid.UUID `path:"roleID" format:"uuid" doc:"Role id"`
	Body   UpdateRoleRequest
}

type SetRolePermissionsRequest struct {
	Permissions []string `json:"permissions" doc:"The complete set of permission keys this role should grant"`
}

type SetRolePermissionsInput struct {
	OrgID  uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	RoleID uuid.UUID `path:"roleID" format:"uuid" doc:"Role id"`
	Body   SetRolePermissionsRequest
}

type ListPermissionsInput struct{}

type ListPermissionsOutput struct {
	Body struct {
		Permissions []PermissionResponse `json:"permissions"`
	}
}

type roleHandlers struct {
	orgs *orgs.Service
}

func registerRoles(api huma.API, service *orgs.Service) {
	h := &roleHandlers{orgs: service}

	huma.Register(api, huma.Operation{
		OperationID: "list-permission-catalog",
		Method:      http.MethodGet,
		Path:        Prefix + "/permissions",
		Summary:     "List every permission the product defines",
		Description: "The catalog the role editor is built from. Self-service: it " +
			"describes the product rather than any organization's data, and a " +
			"caller who cannot see it cannot be shown a meaningful role editor.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   []int{http.StatusUnauthorized},
	}, h.catalog)

	huma.Register(api, huma.Operation{
		OperationID: "list-roles",
		Method:      http.MethodGet,
		Path:        Prefix + "/orgs/{orgID}/roles",
		Summary:     "List roles",
		Description: "Every role in the organization with what it grants and how many " +
			"people hold it. Requires roles.read.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   orgErrors(),
	}, h.list)

	huma.Register(api, huma.Operation{
		OperationID: "get-role",
		Method:      http.MethodGet,
		Path:        Prefix + "/orgs/{orgID}/roles/{roleID}",
		Summary:     "Read a role",
		Description: "Requires roles.read. A role id belonging to another organization " +
			"is 404, not 403.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   orgErrors(),
	}, h.get)

	huma.Register(api, huma.Operation{
		OperationID: "create-role",
		Method:      http.MethodPost,
		Path:        Prefix + "/orgs/{orgID}/roles",
		Summary:     "Define a role",
		Description: "Requires roles.create. Every permission listed must be one the " +
			"caller holds themselves — without that rule, roles.create would be a " +
			"permission to acquire every other permission.",
		Tags:          []string{"organizations"},
		Security:      bearer(),
		DefaultStatus: http.StatusCreated,
		Errors:        append(orgErrors(), http.StatusConflict, http.StatusUnprocessableEntity),
	}, h.create)

	huma.Register(api, huma.Operation{
		OperationID: "update-role",
		Method:      http.MethodPatch,
		Path:        Prefix + "/orgs/{orgID}/roles/{roleID}",
		Summary:     "Rename a role",
		Description: "Requires roles.update. Roles that ship with the product are " +
			"refused with 403: every organization's copy of \"admin\" has to keep " +
			"meaning the same thing. Clone one to get an editable version.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   append(orgErrors(), http.StatusUnprocessableEntity),
	}, h.update)

	huma.Register(api, huma.Operation{
		OperationID: "set-role-permissions",
		Method:      http.MethodPut,
		Path:        Prefix + "/orgs/{orgID}/roles/{roleID}/permissions",
		Summary:     "Replace what a role grants",
		Description: "Sets the permissions to exactly the list given. Requires " +
			"roles.update, refuses roles that ship with the product, and refuses " +
			"any permission the caller does not hold themselves.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   append(orgErrors(), http.StatusUnprocessableEntity),
	}, h.setPermissions)

	huma.Register(api, huma.Operation{
		OperationID: "delete-role",
		Method:      http.MethodDelete,
		Path:        Prefix + "/orgs/{orgID}/roles/{roleID}",
		Summary:     "Delete a role",
		Description: "Requires roles.delete. Refused with 409 while anyone still holds " +
			"it: deleting it anyway would take permissions away from people the " +
			"caller never looked at, and leave nothing behind to explain why.",
		Tags:          []string{"organizations"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors:        append(orgErrors(), http.StatusConflict),
	}, h.remove)
}

// catalog answers from the code, not the database. The catalog is the source of
// truth for which permissions exist, and serving it from a table would let the
// two disagree about what the product can do.
func (h *roleHandlers) catalog(_ context.Context, _ *ListPermissionsInput) (*ListPermissionsOutput, error) {
	defs := authz.Catalog()

	out := &ListPermissionsOutput{}
	out.Body.Permissions = make([]PermissionResponse, 0, len(defs))

	for _, def := range defs {
		out.Body.Permissions = append(out.Body.Permissions, PermissionResponse{
			Key:   string(def.Key),
			Scope: string(def.Scope),
			Group: def.Group,
		})
	}

	return out, nil
}

func (h *roleHandlers) list(ctx context.Context, in *ListRolesInput) (*ListRolesOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	roles, err := h.orgs.Roles(ctx, grant, in.Limit, in.Offset)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	out := &ListRolesOutput{}
	out.Body.Roles = make([]RoleResponse, 0, len(roles))

	for i := range roles {
		out.Body.Roles = append(out.Body.Roles, newRoleResponse(&roles[i]))
	}

	return out, nil
}

func (h *roleHandlers) get(ctx context.Context, in *RolePathInput) (*RoleOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	role, err := h.orgs.Role(ctx, grant, in.RoleID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &RoleOutput{Body: newRoleResponse(role)}, nil
}

func (h *roleHandlers) create(ctx context.Context, in *CreateRoleInput) (*RoleOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	role, err := h.orgs.CreateRole(ctx, grant, in.Body.Key, in.Body.Name, in.Body.Description,
		permissionsFrom(in.Body.Permissions))
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &RoleOutput{Body: newRoleResponse(role)}, nil
}

func (h *roleHandlers) update(ctx context.Context, in *UpdateRoleInput) (*RoleOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	role, err := h.orgs.UpdateRole(ctx, grant, in.RoleID, in.Body.Name, in.Body.Description)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &RoleOutput{Body: newRoleResponse(role)}, nil
}

func (h *roleHandlers) setPermissions(ctx context.Context, in *SetRolePermissionsInput) (*RoleOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	role, err := h.orgs.SetRolePermissions(ctx, grant, in.RoleID, permissionsFrom(in.Body.Permissions))
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &RoleOutput{Body: newRoleResponse(role)}, nil
}

func (h *roleHandlers) remove(ctx context.Context, in *RolePathInput) (*struct{}, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	if err := h.orgs.DeleteRole(ctx, grant, in.RoleID); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}

// permissionsFrom converts the wire strings without filtering.
//
// Dropping unknown keys here would turn "you asked for a permission that does
// not exist" into a silent no-op, and the caller would see a role that does not
// grant what they just saved. The domain refuses them instead, with 422.
func permissionsFrom(keys []string) []authz.Permission {
	out := make([]authz.Permission, 0, len(keys))
	for _, key := range keys {
		out = append(out, authz.Permission(key))
	}

	return out
}
