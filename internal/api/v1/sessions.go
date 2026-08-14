package v1

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/go-example/internal/api/problem"
	"github.com/wokacz/go-example/internal/auth"
	"github.com/wokacz/go-example/internal/domain/user"
)

type CreateSessionInput struct {
	Body CreateSessionRequest
}

type CreateSessionOutput struct {
	Body SessionResponse
}

type sessionHandlers struct {
	users  *user.Service
	tokens *auth.Signer
}

func registerSessions(api huma.API, users *user.Service, tokens *auth.Signer) {
	h := &sessionHandlers{users: users, tokens: tokens}

	huma.Register(api, huma.Operation{
		OperationID: "create-session",
		Method:      http.MethodPost,
		Path:        Prefix + "/sessions",
		Summary:     "Sign in",
		Description: "Exchanges an email and password for a Bearer token. Wrong " +
			"passwords and unknown addresses share one error.",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusCreated,
		Errors: []int{
			http.StatusUnauthorized,
			http.StatusUnprocessableEntity,
			http.StatusTooManyRequests,
		},
	}, h.create)
}

func (h *sessionHandlers) create(ctx context.Context, in *CreateSessionInput) (*CreateSessionOutput, error) {
	u, err := h.users.Authenticate(ctx, in.Body.Email, in.Body.Password)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	if h.tokens == nil {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	token, expires, err := h.tokens.Issue(u.ID, time.Now().UTC())
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &CreateSessionOutput{
		Body: SessionResponse{
			Token:     token,
			ExpiresAt: expires,
			User:      newUserResponse(u),
		},
	}, nil
}
