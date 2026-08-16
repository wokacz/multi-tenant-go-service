package v1

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/audit"
)

// AuditEventResponse is one recorded change.
//
// The actor and the subject carry names and addresses rather than bare ids,
// because the question this endpoint answers is "who did what to whom" and a
// screen full of uuids answers none of it. They are resolved by the same query
// that reads the events.
type AuditEventResponse struct {
	ID      uuid.UUID   `json:"id" format:"uuid"`
	At      time.Time   `json:"at" doc:"When the change was made"`
	Action  string      `json:"action" doc:"What changed, e.g. member.roles_changed"`
	Actor   AuditParty  `json:"actor" doc:"Who made the change"`
	Subject *AuditParty `json:"subject,omitempty" doc:"Who it was about, absent for changes that are not about a person"`

	OrganizationID *uuid.UUID `json:"organization_id,omitempty" format:"uuid"`
	RoleID         *uuid.UUID `json:"role_id,omitempty" format:"uuid"`
	RoleKey        string     `json:"role_key,omitempty" doc:"Captured when the change was made, so it survives the role being deleted"`

	Detail    string `json:"detail,omitempty"`
	IP        string `json:"ip,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
}

type AuditParty struct {
	ID    uuid.UUID `json:"id" format:"uuid"`
	Name  string    `json:"name,omitempty"`
	Email string    `json:"email,omitempty" format:"email"`
}

func newAuditEventResponse(e *audit.Event) AuditEventResponse {
	out := AuditEventResponse{
		ID:             e.ID,
		At:             e.At,
		Action:         string(e.Action),
		Actor:          AuditParty{ID: e.Actor.ID, Name: e.Actor.Name, Email: e.Actor.Email},
		OrganizationID: e.OrganizationID,
		RoleID:         e.RoleID,
		RoleKey:        e.RoleKey,
		Detail:         e.Detail,
		IP:             e.IP,
		UserAgent:      e.UserAgent,
	}

	if e.Subject != nil {
		out.Subject = &AuditParty{ID: e.Subject.ID, Name: e.Subject.Name, Email: e.Subject.Email}
	}

	return out
}

type ListAuditInput struct {
	OrgID  uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	Limit  int       `query:"limit" minimum:"1" maximum:"100" default:"100" doc:"How many to return, newest first"`
	Offset int       `query:"offset" minimum:"0" default:"0" doc:"How many to skip"`
}

type ListAuditOutput struct {
	Body struct {
		Events []AuditEventResponse `json:"events"`
	}
}

type ListPlatformAuditInput struct {
	Limit  int `query:"limit" minimum:"1" maximum:"100" default:"100" doc:"How many to return, newest first"`
	Offset int `query:"offset" minimum:"0" default:"0" doc:"How many to skip"`
}

type auditHandlers struct {
	audit *audit.Service
}

func registerAudit(api huma.API, service *audit.Service) {
	h := &auditHandlers{audit: service}

	huma.Register(api, huma.Operation{
		OperationID: "list-audit-events",
		Method:      http.MethodGet,
		Path:        Prefix + "/orgs/{orgID}/audit",
		Summary:     "Read the organization's history of authorization changes",
		Description: "Who granted or revoked what, and when. Requires audit.read. " +
			"Entries are written in the same transaction as the change they " +
			"describe, so the log cannot disagree with the state.",
		Tags:     []string{"organizations"},
		Security: bearer(),
		Errors:   orgErrors(),
	}, h.list)

	huma.Register(api, huma.Operation{
		OperationID: "list-platform-audit-events",
		Method:      http.MethodGet,
		Path:        Prefix + "/platform/audit",
		Summary:     "Read the installation's history of authorization changes",
		Description: "The same log across every organization, plus the changes that " +
			"belong to no organization. Requires platform.audit.read.",
		Tags:     []string{"platform"},
		Security: bearer(),
		Errors:   platformErrors(),
	}, h.listPlatform)
}

func (h *auditHandlers) list(ctx context.Context, in *ListAuditInput) (*ListAuditOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	events, err := h.audit.Events(ctx, grant.OrganizationID(), in.Limit, in.Offset)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return auditOutput(events), nil
}

func (h *auditHandlers) listPlatform(ctx context.Context, in *ListPlatformAuditInput) (*ListAuditOutput, error) {
	events, err := h.audit.AllEvents(ctx, in.Limit, in.Offset)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return auditOutput(events), nil
}

func auditOutput(events []audit.Event) *ListAuditOutput {
	out := &ListAuditOutput{}
	out.Body.Events = make([]AuditEventResponse, 0, len(events))

	for i := range events {
		out.Body.Events = append(out.Body.Events, newAuditEventResponse(&events[i]))
	}

	return out
}
