package v1

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/go-example/internal/api/problem"
	"github.com/wokacz/go-example/internal/domain/user"
	"github.com/wokacz/go-example/internal/mail"
)

type RequestPasswordResetInput struct {
	Body RequestPasswordResetRequest
}

type ConfirmPasswordResetInput struct {
	Body ConfirmPasswordResetRequest
}

type passwordResetHandlers struct {
	users *user.Service
	mail  mail.Sender
}

func registerPasswordResets(api huma.API, users *user.Service, mailer mail.Sender) {
	h := &passwordResetHandlers{users: users, mail: mailer}

	huma.Register(api, huma.Operation{
		OperationID: "request-password-reset",
		Method:      http.MethodPost,
		Path:        Prefix + "/password-resets",
		Summary:     "Request a password reset",
		Description: "Sends a one-time code to the address if it is registered. " +
			"The response is always 204 so the call cannot be used to discover " +
			"accounts. In development without SMTP the code is written to the " +
			"process log.",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnprocessableEntity, http.StatusTooManyRequests},
	}, h.request)

	huma.Register(api, huma.Operation{
		OperationID: "confirm-password-reset",
		Method:      http.MethodPost,
		Path:        Prefix + "/password-resets/confirm",
		Summary:     "Confirm a password reset",
		Description: "Spends the code and sets a new password. password and " +
			"password_confirm must match. Unknown addresses, expired codes and " +
			"wrong codes share one error.",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusNoContent,
		Errors: []int{
			http.StatusUnauthorized,
			http.StatusUnprocessableEntity,
			http.StatusTooManyRequests,
		},
	}, h.confirm)
}

func (h *passwordResetHandlers) request(ctx context.Context, in *RequestPasswordResetInput) (*struct{}, error) {
	code, err := h.users.BeginPasswordReset(ctx, in.Body.Email)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	if code != "" && h.mail != nil {
		if err := h.mail.SendPasswordReset(ctx, user.NormalizeEmail(in.Body.Email), code); err != nil {
			return nil, problem.Error(ctx, err)
		}
	}

	return nil, nil
}

func (h *passwordResetHandlers) confirm(ctx context.Context, in *ConfirmPasswordResetInput) (*struct{}, error) {
	err := h.users.CompletePasswordReset(
		ctx,
		in.Body.Email,
		in.Body.Code,
		in.Body.Password,
		in.Body.PasswordConfirm,
	)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}
