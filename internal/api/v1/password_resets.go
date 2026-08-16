package v1

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/mail"
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
	log   *slog.Logger
}

func registerPasswordResets(api huma.API, users *user.Service, mailer mail.Sender, log *slog.Logger) {
	h := &passwordResetHandlers{users: users, mail: mailer, log: log}

	huma.Register(api, huma.Operation{
		OperationID: "request-password-reset",
		Method:      http.MethodPost,
		Path:        Prefix + "/password-resets",
		Summary:     "Request a password reset",
		Description: "Sends a one-time code to the address if it is registered. " +
			"The response is always 204 — including when delivery or storage " +
			"fails — so the call cannot be used to discover accounts. In " +
			"development without SMTP the code is written to the process log.",
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
	// Persistence and delivery failures must not change the status: a 5xx
	// here would only fire for registered addresses, which is how callers
	// would otherwise enumerate accounts. Validation errors never reach this
	// handler — huma rejects a malformed body first.
	code, err := h.users.BeginPasswordReset(ctx, in.Body.Email)
	if err != nil {
		logger(h.log).ErrorContext(ctx, "password reset request failed", "error", err)
		return nil, nil
	}

	if code != "" && h.mail != nil {
		if err := h.mail.SendPasswordReset(ctx, user.NormalizeEmail(in.Body.Email), code); err != nil {
			logger(h.log).ErrorContext(ctx, "password reset mail failed", "error", err)
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
