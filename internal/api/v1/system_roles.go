package v1

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
)

// SystemRoleHolderResponse is one installation-wide grant.
//
// The name and the address are here rather than a bare id for the same reason the
// audit log carries them: the question this answers is "who administers this
// installation", and a screen full of uuids answers none of it.
type SystemRoleHolderResponse struct {
	UserID    uuid.UUID  `json:"user_id" format:"uuid"`
	Name      string     `json:"name,omitempty"`
	Email     string     `json:"email,omitempty" format:"email"`
	RoleKey   string     `json:"role_key" doc:"Installation-wide role held, e.g. platform_admin"`
	GrantedBy *uuid.UUID `json:"granted_by,omitempty" format:"uuid" doc:"Who granted it, absent when it came from the bootstrap command"`
	GrantedAt time.Time  `json:"granted_at"`
}

type ListSystemRolesInput struct{}

type ListSystemRolesOutput struct {
	Body struct {
		Holders []SystemRoleHolderResponse `json:"holders"`
	}
}

// GrantSystemRoleRequest names the account and the role.
//
// The role is a key rather than an id: installation-wide roles are defined in code,
// not stored per organization, so there is no row to point at.
type GrantSystemRoleRequest struct {
	UserID  uuid.UUID `json:"user_id" format:"uuid" doc:"Account to grant it to"`
	RoleKey string    `json:"role_key" minLength:"1" maxLength:"64" doc:"Installation-wide role key"`
}

type GrantSystemRoleInput struct {
	Body GrantSystemRoleRequest
}

type RevokeSystemRoleInput struct {
	UserID  uuid.UUID `path:"userID" format:"uuid" doc:"Account to take it from"`
	RoleKey string    `path:"roleKey" maxLength:"64" doc:"Installation-wide role key"`
}

type systemRoleHandlers struct {
	orgs *orgs.Service
}

func registerSystemRoles(api huma.API, service *orgs.Service) {
	h := &systemRoleHandlers{orgs: service}

	huma.Register(api, huma.Operation{
		OperationID: "list-system-roles",
		Method:      http.MethodGet,
		Path:        Prefix + "/platform/system-roles",
		Summary:     "List who administers the installation",
		Description: "Every installation-wide role grant, with who granted it and when. " +
			"Requires platform.system_roles.read. Until this existed there was no " +
			"way to find out who held platform_admin without reading the database.",
		Tags:     []string{"platform"},
		Security: bearer(),
		Errors:   platformErrors(),
	}, h.list)

	huma.Register(api, huma.Operation{
		OperationID: "grant-system-role",
		Method:      http.MethodPost,
		Path:        Prefix + "/platform/system-roles",
		Summary:     "Grant an installation-wide role",
		Description: "Makes an account an administrator of the whole installation. " +
			"Requires platform.system_roles.assign. Granting twice is not an error " +
			"and records nothing the second time. This is the most consequential " +
			"change available here — platform_admin covers every platform " +
			"permission — and it is written to the audit log.",
		Tags:          []string{"platform"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors:        append(platformErrors(), http.StatusUnprocessableEntity),
	}, h.grant)

	huma.Register(api, huma.Operation{
		OperationID: "revoke-system-role",
		Method:      http.MethodDelete,
		Path:        Prefix + "/platform/system-roles/{userID}/{roleKey}",
		Summary:     "Revoke an installation-wide role",
		Description: "Requires platform.system_roles.remove. Revoking one that was " +
			"never granted is not an error. Revoking your own last one is refused " +
			"with 409: it would take away the permission needed to grant it back, " +
			"and with no other holder nobody could.",
		Tags:          []string{"platform"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors:        append(platformErrors(), http.StatusConflict, http.StatusUnprocessableEntity),
	}, h.revoke)
}

func (h *systemRoleHandlers) list(ctx context.Context, _ *ListSystemRolesInput) (*ListSystemRolesOutput, error) {
	holders, err := h.orgs.SystemRoleHolders(ctx)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	out := &ListSystemRolesOutput{}
	out.Body.Holders = make([]SystemRoleHolderResponse, 0, len(holders))

	for i := range holders {
		out.Body.Holders = append(out.Body.Holders, SystemRoleHolderResponse{
			UserID:    holders[i].UserID,
			Name:      holders[i].Name,
			Email:     holders[i].Email,
			RoleKey:   holders[i].RoleKey,
			GrantedBy: holders[i].GrantedBy,
			GrantedAt: holders[i].GrantedAt,
		})
	}

	return out, nil
}

func (h *systemRoleHandlers) grant(ctx context.Context, in *GrantSystemRoleInput) (*struct{}, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	err = h.orgs.GrantSystemRole(ctx, grant, in.Body.UserID, authz.RoleKey(in.Body.RoleKey))
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}

func (h *systemRoleHandlers) revoke(ctx context.Context, in *RevokeSystemRoleInput) (*struct{}, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	if err := h.orgs.RevokeSystemRole(ctx, grant, in.UserID, authz.RoleKey(in.RoleKey)); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}
