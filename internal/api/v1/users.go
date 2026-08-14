package v1

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/go-example/internal/api/apierr"
	"github.com/wokacz/go-example/internal/user"
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

type CreateUserOutput struct {
	// Location points at the created resource, so a client can follow up
	// without reassembling the URL from the body.
	Location string `header:"Location"`
	Body     UserResponse
}

type userHandlers struct {
	users *user.Service
}

func registerUsers(api huma.API, users *user.Service) {
	h := &userHandlers{users: users}

	huma.Register(api, huma.Operation{
		OperationID: "get-user",
		Method:      http.MethodGet,
		Path:        Prefix + "/users/{id}",
		Summary:     "Fetch a user",
		Tags:        []string{"users"},
		// Every status the handler can produce has to be listed, or it is
		// missing from the spec and from any generated client.
		Errors: []int{http.StatusNotFound},
	}, h.get)

	huma.Register(api, huma.Operation{
		OperationID:   "create-user",
		Method:        http.MethodPost,
		Path:          Prefix + "/users",
		Summary:       "Register a user",
		Tags:          []string{"users"},
		DefaultStatus: http.StatusCreated,
		Errors:        []int{http.StatusConflict, http.StatusUnprocessableEntity},
	}, h.create)
}

func (h *userHandlers) get(ctx context.Context, in *GetUserInput) (*GetUserOutput, error) {
	u, err := h.users.ByID(ctx, in.ID)
	if err != nil {
		// The handler never decides a status itself; apierr owns that mapping.
		return nil, apierr.Error(ctx, err)
	}

	return &GetUserOutput{Body: newUserResponse(u)}, nil
}

func (h *userHandlers) create(ctx context.Context, in *CreateUserInput) (*CreateUserOutput, error) {
	u, err := h.users.Create(ctx, in.Body.Name, in.Body.Email, in.Body.Password)
	if err != nil {
		return nil, apierr.Error(ctx, err)
	}

	return &CreateUserOutput{
		Location: Prefix + "/users/" + u.ID.String(),
		Body:     newUserResponse(u),
	}, nil
}
