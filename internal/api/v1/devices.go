package v1

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
)

// maxLoginEventLimit mirrors the domain's cap. The handler does not enforce it
// — the service clamps — but the schema has to advertise the same number, or
// clients build paging around a limit that is silently ignored.
const maxLoginEventLimit = 50

// A struct tag cannot reference a constant, so the documented maximum and the
// one the service enforces are tied together the same way dto.go ties the
// password rules: one of these subtractions underflows if they disagree, and
// the package stops compiling.
const (
	_ = uint(user.MaxLoginEvents - maxLoginEventLimit)
	_ = uint(maxLoginEventLimit - user.MaxLoginEvents)
)

type ListDevicesInput struct{}

type ListDevicesOutput struct {
	Body struct {
		Devices []DeviceResponse `json:"devices"`
	}
}

type RevokeDeviceInput struct {
	ID uuid.UUID `path:"id" format:"uuid" doc:"Device id"`
}

type ListLoginEventsInput struct {
	Limit int `query:"limit" minimum:"1" maximum:"50" default:"50" doc:"How many entries to return, newest first"`
}

type ListLoginEventsOutput struct {
	Body struct {
		Events []LoginEventResponse `json:"events"`
	}
}

type deviceHandlers struct {
	users *user.Service
}

func registerDevices(api huma.API, users *user.Service) {
	h := &deviceHandlers{users: users}

	huma.Register(api, huma.Operation{
		OperationID: "list-devices",
		Method:      http.MethodGet,
		Path:        Prefix + "/me/devices",
		Summary:     "List known devices",
		Description: "Returns every device that has signed in to the account, most " +
			"recently seen first, including revoked ones so the owner can see " +
			"what was blocked.",
		Tags: []string{"devices"},
		Security: []map[string][]string{
			{"bearer": {}},
		},
		Errors: []int{http.StatusUnauthorized},
	}, h.list)

	huma.Register(api, huma.Operation{
		OperationID: "revoke-device",
		Method:      http.MethodDelete,
		Path:        Prefix + "/me/devices/{id}",
		Summary:     "Revoke a device",
		Description: "Blocks the device and drops its trust. Tokens already issued " +
			"to it stop working on the next request rather than at expiry. " +
			"Revoking the device making the call is allowed, and is how a " +
			"client signs itself out. Revoking twice succeeds. An id that is " +
			"not the caller's is 404.",
		Tags: []string{"devices"},
		Security: []map[string][]string{
			{"bearer": {}},
		},
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusNotFound},
	}, h.revoke)

	huma.Register(api, huma.Operation{
		OperationID: "list-login-events",
		Method:      http.MethodGet,
		Path:        Prefix + "/me/login-events",
		Summary:     "List sign-in history",
		Description: "Returns the account's recent sign-in attempts, newest first. " +
			"Attempts against an address that is not registered are not " +
			"recorded, so an empty history means nobody has tried this account.",
		Tags: []string{"devices"},
		Security: []map[string][]string{
			{"bearer": {}},
		},
		Errors: []int{http.StatusUnauthorized},
	}, h.loginEvents)
}

func (h *deviceHandlers) list(ctx context.Context, _ *ListDevicesInput) (*ListDevicesOutput, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	devices, err := h.users.Devices(ctx, sess.UserID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	out := &ListDevicesOutput{}
	// Built with make so an account with no devices serialises as [] rather
	// than null. A client that maps over the result should not have to
	// special-case the empty case.
	out.Body.Devices = make([]DeviceResponse, 0, len(devices))

	for i := range devices {
		out.Body.Devices = append(out.Body.Devices, newDeviceResponse(&devices[i], sess.DeviceID))
	}

	return out, nil
}

func (h *deviceHandlers) revoke(ctx context.Context, in *RevokeDeviceInput) (*struct{}, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	if err := h.users.RevokeDevice(ctx, sess.UserID, in.ID); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}

func (h *deviceHandlers) loginEvents(ctx context.Context, in *ListLoginEventsInput) (*ListLoginEventsOutput, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	events, err := h.users.LoginEvents(ctx, sess.UserID, in.Limit)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	out := &ListLoginEventsOutput{}
	out.Body.Events = make([]LoginEventResponse, 0, len(events))

	for i := range events {
		out.Body.Events = append(out.Body.Events, newLoginEventResponse(&events[i]))
	}

	return out, nil
}
