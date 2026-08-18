package v1

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/mail"
)

// BeginEmailChangeRequest asks for a code to be sent to a new address.
//
// The current password is required for the same reason SetTwoFactorRequest
// requires it: the address on an account is where a password reset goes, so a
// token that leaked out of a browser must not be enough to redirect it.
type BeginEmailChangeRequest struct {
	NewEmail string `json:"new_email" format:"email" minLength:"3" maxLength:"255" doc:"Address to move the account to"`
	Password string `json:"password" minLength:"1" maxLength:"72" doc:"Current password"`
}

type BeginEmailChangeInput struct {
	Body BeginEmailChangeRequest
}

// ConfirmEmailChangeRequest carries the code from the new mailbox.
type ConfirmEmailChangeRequest struct {
	Code string `json:"code" minLength:"1" maxLength:"12" doc:"Code from the message sent to the new address"`
}

type ConfirmEmailChangeInput struct {
	Body ConfirmEmailChangeRequest
}

type emailChangeHandlers struct {
	users *user.Service
	mail  mail.Sender
	log   *slog.Logger
}

func registerEmailChange(api huma.API, users *user.Service, sender mail.Sender, log *slog.Logger) {
	h := &emailChangeHandlers{users: users, mail: sender, log: log}

	huma.Register(api, huma.Operation{
		OperationID: "begin-email-change",
		Method:      http.MethodPost,
		Path:        Prefix + "/me/email",
		Summary:     "Ask to change the account's address",
		Description: "Sends a confirmation code to the new address. The account keeps " +
			"its current address until the code comes back, so an address nobody " +
			"has answered on never becomes the one that receives password resets. " +
			"Requires the current password. Whether the new address is already " +
			"registered is not disclosed here — that answer is given only to " +
			"somebody who can read the code out of the mailbox.",
		Tags: []string{"auth"},
		Security: []map[string][]string{
			{"bearer": {}},
		},
		DefaultStatus: http.StatusNoContent,
		Errors: []int{
			http.StatusUnauthorized,
			http.StatusNotFound,
			http.StatusUnprocessableEntity,
			http.StatusTooManyRequests,
		},
	}, h.begin)

	huma.Register(api, huma.Operation{
		OperationID: "confirm-email-change",
		Method:      http.MethodPost,
		Path:        Prefix + "/me/email/verify",
		Summary:     "Confirm the account's new address",
		Description: "Applies the address the code was sent to. A wrong, expired or " +
			"already-spent code, and no outstanding change at all, share one " +
			"answer. Existing sessions are deliberately left alone: the password " +
			"has not changed, so signing every device out would be a surprise " +
			"with no security benefit. 409 means the address was claimed by " +
			"somebody else while the code was in flight.",
		Tags: []string{"auth"},
		Security: []map[string][]string{
			{"bearer": {}},
		},
		DefaultStatus: http.StatusNoContent,
		Errors: []int{
			http.StatusUnauthorized,
			http.StatusNotFound,
			http.StatusConflict,
			http.StatusUnprocessableEntity,
		},
	}, h.confirm)
}

func (h *emailChangeHandlers) begin(ctx context.Context, in *BeginEmailChangeInput) (*struct{}, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	code, err := h.users.BeginEmailChange(ctx, sess.UserID, in.Body.NewEmail, in.Body.Password)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	// The address the code was sent to comes back from the service rather than
	// from the request, so a difference in normalisation cannot send the code
	// somewhere other than where the change will land.
	to, err := h.users.PendingEmailChange(ctx, sess.UserID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	// A failed send is logged and not reported, the same way the password-reset
	// path treats it: the response must not depend on anything about the address,
	// and a caller who sees "sent" and gets nothing retries, which is the correct
	// behaviour anyway.
	if err := h.mail.SendEmailChange(ctx, to, code); err != nil {
		h.log.ErrorContext(ctx, "sending email change code", "error", err)
	}

	return nil, nil
}

func (h *emailChangeHandlers) confirm(ctx context.Context, in *ConfirmEmailChangeInput) (*struct{}, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	if err := h.users.ConfirmEmailChange(ctx, sess.UserID, in.Body.Code); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}
