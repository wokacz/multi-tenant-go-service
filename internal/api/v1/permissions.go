package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/i18n"
)

// PermissionSnapshotResponse is everything the caller may do, everywhere.
//
// It exists so a client can decide what to render before the user clicks. It is
// not, and must never become, the thing that decides whether an action is
// allowed: the server re-resolves permissions on every request, and a client
// working from a stale snapshot simply receives a 403 it is expected to handle.
// Hiding a button is a courtesy; refusing the request is the control.
type PermissionSnapshotResponse struct {
	User   SnapshotUser         `json:"user"`
	System SnapshotSystem       `json:"system"`
	Orgs   []SnapshotMembership `json:"organizations"`
}

type SnapshotUser struct {
	ID     uuid.UUID `json:"id" format:"uuid"`
	Locale string    `json:"locale" doc:"Language this response was written in"`
}

type SnapshotSystem struct {
	Roles       []string `json:"roles" doc:"Installation-wide role keys, empty for almost everyone"`
	Permissions []string `json:"permissions" doc:"Installation-wide permission keys"`
}

// SnapshotMembership is one organization as the caller sees it.
//
// Organizations the caller is suspended in are listed with their status and an
// empty permission set. Dropping them would make them vanish from the UI with no
// explanation; pretending they grant something would be worse.
//
// Invitations are absent: they are not memberships, and GET /v1/me/invitations is
// where a client finds them.
type SnapshotMembership struct {
	ID          uuid.UUID `json:"id" format:"uuid"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Status      string    `json:"status" enum:"active,suspended"`
	Roles       []string  `json:"roles" doc:"Role keys held here"`
	Permissions []string  `json:"permissions" doc:"Permission keys held here"`
}

type GetMyPermissionsInput struct {
	IfNoneMatch string `header:"If-None-Match" doc:"ETag from a previous response; a match answers 304"`
}

type GetMyPermissionsOutput struct {
	ETag string `header:"ETag" doc:"Changes whenever the caller's permissions change"`
	Body PermissionSnapshotResponse
}

type permissionHandlers struct {
	orgs      *orgs.Service
	snapshots authz.Snapshotter
}

func registerPermissions(api huma.API, service *orgs.Service, snapshots authz.Snapshotter) {
	h := &permissionHandlers{orgs: service, snapshots: snapshots}

	huma.Register(api, huma.Operation{
		OperationID: "get-my-permissions",
		Method:      http.MethodGet,
		Path:        Prefix + "/me/permissions",
		Summary:     "What the caller may do, everywhere",
		Description: "The snapshot a client uses to decide what to render. Self-service: " +
			"no permission is required and none can take it away. It is a hint, " +
			"not a decision — every request is authorized again on the server, so " +
			"a client holding a stale snapshot receives a 403 and should refresh. " +
			"Carries an ETag; send it back as If-None-Match to get a 304.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   []int{http.StatusUnauthorized},
	}, h.mine)
}

func (h *permissionHandlers) mine(ctx context.Context, in *GetMyPermissionsInput) (*GetMyPermissionsOutput, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	snapshot, err := h.snapshots.Snapshot(ctx, sess.UserID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	memberships, err := h.orgs.Mine(ctx, sess.UserID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	body := PermissionSnapshotResponse{
		User: SnapshotUser{ID: sess.UserID, Locale: string(i18n.LocaleFrom(ctx))},
		System: SnapshotSystem{
			Roles:       roleKeyStrings(snapshot.SystemRoles),
			Permissions: permissionStrings(snapshot.SystemPermissions),
		},
		Orgs: make([]SnapshotMembership, 0, len(memberships)),
	}

	for i := range memberships {
		membership := &memberships[i]

		roles := membership.RoleKeys
		if roles == nil {
			roles = []string{}
		}

		body.Orgs = append(body.Orgs, SnapshotMembership{
			ID:          membership.Organization.ID,
			Slug:        membership.Organization.Slug,
			Name:        membership.Organization.Name,
			Status:      string(membership.Status),
			Roles:       roles,
			Permissions: permissionStrings(snapshot.Permissions(membership.Organization.ID)),
		})
	}

	etag, err := etagOf(body)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	// The tag is a hash of the answer rather than a counter bumped on every
	// write. A counter is cheaper to compare but has to be incremented in every
	// path that changes a role, a membership or an organization's name — miss
	// one and the client caches a snapshot that is quietly wrong, with no
	// symptom. Hashing the payload cannot be wrong; it only costs the query that
	// was going to happen anyway.
	if in.IfNoneMatch != "" && in.IfNoneMatch == etag {
		return nil, huma.Status304NotModified()
	}

	return &GetMyPermissionsOutput{ETag: etag, Body: body}, nil
}

func etagOf(body PermissionSnapshotResponse) (string, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(encoded)

	// Weak, because this compares a rendering of the resource rather than its
	// bytes: the same permissions serialise identically, but nothing here
	// promises byte equality across releases.
	return `W/"` + hex.EncodeToString(sum[:16]) + `"`, nil
}

func permissionStrings(permissions []authz.Permission) []string {
	out := make([]string, 0, len(permissions))
	for _, perm := range permissions {
		out = append(out, string(perm))
	}

	return out
}

func roleKeyStrings(keys []authz.RoleKey) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, string(key))
	}

	return out
}
