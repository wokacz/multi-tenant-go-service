package v1

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/go-example/internal/api/problem"
	"github.com/wokacz/go-example/internal/auth"
	"github.com/wokacz/go-example/internal/domain/user"
)

type GetUserInput struct {
	ID uuid.UUID `path:"id" format:"uuid" doc:"User id"`
}

type GetUserOutput struct {
	Body UserResponse
}

type CreateUserInput struct {
	Body CreateUserRequest
}

func registerUsers(api huma.API, users *user.Service) {
	h := &userHandlers{users: users}

	huma.Register(api, huma.Operation{
		OperationID: "get-user",
		Method:      http.MethodGet,
		Path:        Prefix + "/users/{id}",
		Summary:     "Fetch a user",
		Description: "Returns the authenticated user's own record. Another user's " +
			"id is indistinguishable from a missing one.",
		Tags: []string{"users"},
		Security: []map[string][]string{
			{"bearer": {}},
		},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
	}, h.get)

	huma.Register(api, huma.Operation{
		OperationID: "create-user",
		Method:      http.MethodPost,
		Path:        Prefix + "/users",
		Summary:     "Register a user",
		Description: "Creates an account. A duplicate email is reported as success " +
			"so the response cannot be used to discover registered addresses. " +
			"Sign in with POST /v1/sessions to obtain a token.",
		Tags:          []string{"users"},
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnprocessableEntity, http.StatusTooManyRequests},
	}, h.create)
}

type userHandlers struct {
	users *user.Service
}

func (h *userHandlers) get(ctx context.Context, in *GetUserInput) (*GetUserOutput, error) {
	caller, ok := auth.UserIDFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	// Do not look up another user's id: a 403-vs-404 split would disclose
	// whether that account exists.
	if caller != in.ID {
		return nil, problem.Error(ctx, user.ErrNotFound)
	}

	u, err := h.users.ByID(ctx, in.ID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &GetUserOutput{Body: newUserResponse(u)}, nil
}

func (h *userHandlers) create(ctx context.Context, in *CreateUserInput) (*struct{}, error) {
	_, err := h.users.Create(ctx, in.Body.Name, in.Body.Email, in.Body.Password)
	if err != nil && !errors.Is(err, user.ErrEmailTaken) {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}
