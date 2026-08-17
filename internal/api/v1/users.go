package v1

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/i18n"
)

type GetUserInput struct {
	ID uuid.UUID `path:"id" format:"uuid" doc:"User id"`
}

type GetMeInput struct{}

type GetUserOutput struct {
	Body UserResponse
}

type CreateUserInput struct {
	// AcceptLanguage is read rather than negotiated from the context because
	// only this handler needs to tell "the client asked for a language" apart
	// from "the client said nothing and got the default". Declaring it here also
	// puts it in the contract, which is where a client should learn that signing
	// up sets the account's language.
	AcceptLanguage string `header:"Accept-Language" doc:"Language to remember for this account. Absent means no preference, and later requests keep negotiating per request."`

	Body CreateUserRequest
}

func registerUsers(api huma.API, users *user.Service, service *orgs.Service) {
	h := &userHandlers{users: users, orgs: service}

	huma.Register(api, huma.Operation{
		OperationID: "get-me",
		Method:      http.MethodGet,
		Path:        Prefix + "/me",
		Summary:     "Fetch the authenticated user",
		Description: "Returns the account that owns the Bearer token.",
		Tags:        []string{"users"},
		Security: []map[string][]string{
			{"bearer": {}},
		},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
	}, h.me)

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
		Description: "Creates an account. password and password_confirm must match. " +
			"A duplicate email is reported as success so the response cannot be " +
			"used to discover registered addresses. Sign in with POST /v1/sessions " +
			"to obtain a token.",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnprocessableEntity, http.StatusTooManyRequests},
	}, h.create)
}

type userHandlers struct {
	users *user.Service
	orgs  *orgs.Service
}

func (h *userHandlers) me(ctx context.Context, _ *GetMeInput) (*GetUserOutput, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	u, err := h.users.ByID(ctx, sess.UserID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &GetUserOutput{Body: newUserResponse(u)}, nil
}

func (h *userHandlers) get(ctx context.Context, in *GetUserInput) (*GetUserOutput, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	// Do not look up another user's id: a 403-vs-404 split would disclose
	// whether that account exists.
	if sess.UserID != in.ID {
		return nil, problem.Error(ctx, user.ErrNotFound)
	}

	u, err := h.users.ByID(ctx, in.ID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &GetUserOutput{Body: newUserResponse(u)}, nil
}

func (h *userHandlers) create(ctx context.Context, in *CreateUserInput) (*struct{}, error) {
	// Only a language the client actually asked for, and only one this build can
	// render. Storing the fallback for somebody who expressed no preference
	// would turn a guess into a permanent choice, and their browser would be
	// ignored from then on.
	locale := ""
	if matched, ok := i18n.Default().Match(in.AcceptLanguage); ok {
		locale = string(matched)
	}

	created, err := h.users.Create(ctx, in.Body.Name, in.Body.Email, in.Body.Password, in.Body.PasswordConfirm, locale)
	if err != nil {
		// A duplicate address answers 204 like a success, so the status cannot
		// be used to discover which addresses are registered.
		if errors.Is(err, user.ErrEmailTaken) {
			return nil, nil
		}

		return nil, problem.Error(ctx, err)
	}

	// A brand new account belongs to nothing and can therefore do nothing, which
	// looks exactly like a broken sign-up. Joining the default organization as a
	// plain member is what makes an installation that never configures
	// organizations behave like one that has no organizations at all.
	if h.orgs != nil {
		if err := h.orgs.AttachInvitations(ctx, created.ID, created.Email); err != nil {
			return nil, problem.Error(ctx, err)
		}

		if err := h.orgs.JoinDefaultOrganization(ctx, created.ID); err != nil {
			return nil, problem.Error(ctx, err)
		}
	}

	return nil, nil
}
