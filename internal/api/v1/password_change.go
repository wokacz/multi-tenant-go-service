package v1

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
)

// ChangePasswordRequest replaces the password from inside a session.
//
// The current password is required for the same reason BeginEmailChangeRequest
// requires it: a bearer token that leaked out of a browser must not be enough to
// make the change that locks the owner out of their own account.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" minLength:"1" maxLength:"72" doc:"Current password"`
	Password        string `json:"password" minLength:"12" maxLength:"72" doc:"New password"`
	PasswordConfirm string `json:"password_confirm" minLength:"12" maxLength:"72" doc:"New password again"`
}

type ChangePasswordInput struct {
	Body ChangePasswordRequest
}

type SignOutEverywhereInput struct{}

type passwordChangeHandlers struct {
	users *user.Service
}

func registerPasswordChange(api huma.API, users *user.Service) {
	h := &passwordChangeHandlers{users: users}

	huma.Register(api, huma.Operation{
		OperationID: "change-password",
		Method:      http.MethodPost,
		Path:        Prefix + "/me/password",
		Summary:     "Change the caller's own password",
		Description: "Requires the current password. Until now the only way to change " +
			"a password was to forget it and go through POST /v1/password-resets, " +
			"which needs access to the mailbox. " +
			"Every token issued for the account stops working, this request's own " +
			"included, so the client has to sign in again — the same thing a reset " +
			"already did. Trusted devices are untouched: this ends sessions, and " +
			"DELETE /v1/me/devices/{id} is what ends a device.",
		Tags:          []string{"auth"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors: []int{
			http.StatusUnauthorized,
			http.StatusNotFound,
			http.StatusUnprocessableEntity,
			http.StatusTooManyRequests,
		},
	}, h.change)

	huma.Register(api, huma.Operation{
		OperationID: "sign-out-everywhere",
		Method:      http.MethodDelete,
		Path:        Prefix + "/me/sessions",
		Summary:     "Sign out of every session",
		Description: "Invalidates every token issued for the account, including the " +
			"one making the request. For somebody who thinks a session is open on a " +
			"machine they no longer have — which is not a reason to change a " +
			"password they still trust. " +
			"There is nothing to list at this path: sessions are tokens, and the " +
			"account's session epoch is the only state behind them. Trusted devices " +
			"are a separate list at GET /v1/me/devices.",
		Tags:          []string{"auth"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusNotFound},
	}, h.signOutEverywhere)
}

func (h *passwordChangeHandlers) change(ctx context.Context, in *ChangePasswordInput) (*struct{}, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	err := h.users.ChangePassword(ctx, sess.UserID,
		in.Body.CurrentPassword, in.Body.Password, in.Body.PasswordConfirm)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}

func (h *passwordChangeHandlers) signOutEverywhere(ctx context.Context, _ *SignOutEverywhereInput) (*struct{}, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	if err := h.users.SignOutEverywhere(ctx, sess.UserID); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}
