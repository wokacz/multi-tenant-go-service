package v1

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
)

type SetTwoFactorInput struct {
	DeviceToken string `header:"X-Device-Token" doc:"Device token of the calling client, trusted when enabling"`
	Body        SetTwoFactorRequest
}

type twoFactorHandlers struct {
	users *user.Service
}

func registerTwoFactor(api huma.API, users *user.Service) {
	h := &twoFactorHandlers{users: users}

	huma.Register(api, huma.Operation{
		OperationID: "set-two-factor",
		Method:      http.MethodPut,
		Path:        Prefix + "/me/two-factor",
		Summary:     "Turn two-factor on or off",
		Description: "Requires the current password as well as the token. Enabling " +
			"also trusts the calling device, so an account whose address no " +
			"longer receives mail is not locked out by its own setting; every " +
			"other device will be asked for a code on its next sign-in.",
		Tags: []string{"auth"},
		Security: []map[string][]string{
			{"bearer": {}},
		},
		DefaultStatus: http.StatusNoContent,
		Errors: []int{
			http.StatusUnauthorized,
			http.StatusNotFound,
			http.StatusUnprocessableEntity,
		},
	}, h.set)
}

func (h *twoFactorHandlers) set(ctx context.Context, in *SetTwoFactorInput) (*struct{}, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	err := h.users.SetTwoFactor(ctx, sess.UserID, in.Body.Password, in.Body.Enabled,
		signInContext(ctx, in.DeviceToken))
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}
