package v1

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/files"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
)

type SetAvatarInput struct {
	RawBody huma.MultipartFormFiles[struct {
		File huma.FormFile `form:"file" required:"true" doc:"The image to store as the account photo"`
	}]
}

type SetAvatarOutput struct {
	Body AvatarResponse
}

type GetAvatarInput struct{}

type GetAvatarOutput struct {
	ContentType   string `header:"Content-Type"`
	ContentLength string `header:"Content-Length"`
	Body          []byte
}

type DeleteAvatarInput struct{}

type avatarHandlers struct {
	files *files.Service
}

func registerAvatar(api huma.API, service *files.Service) {
	h := &avatarHandlers{files: service}

	huma.Register(api, huma.Operation{
		OperationID: "set-avatar",
		Method:      http.MethodPost,
		Path:        Prefix + "/me/avatar",
		Summary:     "Set the account photo",
		Description: "Self-service: no permission is required. The payload is sniffed " +
			"from magic bytes, optionally scanned, encrypted at rest, and stored. " +
			"Only png, jpeg, gif and webp are accepted. A second call replaces the " +
			"previous photo.",
		Tags:     []string{"users"},
		Security: bearer(),
		Errors: []int{
			http.StatusUnauthorized,
			http.StatusNotFound,
			http.StatusRequestEntityTooLarge,
			http.StatusUnprocessableEntity,
			http.StatusServiceUnavailable,
		},
	}, h.set)

	huma.Register(api, huma.Operation{
		OperationID: "get-avatar",
		Method:      http.MethodGet,
		Path:        Prefix + "/me/avatar",
		Summary:     "Download the account photo",
		Description: "Self-service. Decrypts the stored blob and returns it with the " +
			"detected Content-Type. Missing is 404, the same as a missing account.",
		Tags:     []string{"users"},
		Security: bearer(),
		Errors:   []int{http.StatusUnauthorized, http.StatusNotFound},
		Responses: map[string]*huma.Response{
			"200": {
				Description: "Decrypted account photo",
				Content: map[string]*huma.MediaType{
					"image/png":  {},
					"image/jpeg": {},
					"image/gif":  {},
					"image/webp": {},
				},
			},
		},
	}, h.get)

	huma.Register(api, huma.Operation{
		OperationID:   "delete-avatar",
		Method:        http.MethodDelete,
		Path:          Prefix + "/me/avatar",
		Summary:       "Remove the account photo",
		Description:   "Self-service. Removes the ciphertext and the files row, and clears users.avatar_id.",
		Tags:          []string{"users"},
		Security:      bearer(),
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusNotFound},
	}, h.delete)
}

func (h *avatarHandlers) set(ctx context.Context, in *SetAvatarInput) (*SetAvatarOutput, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	form := in.RawBody.Data()
	part := form.File
	if part.File != nil {
		defer func() { _ = part.Close() }()
	}

	content, err := io.ReadAll(part)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, problem.Error(ctx, files.ErrTooLarge)
		}

		return nil, problem.Error(ctx, err)
	}

	avatar, err := h.files.SetAvatar(ctx, sess.UserID, part.Filename, part.ContentType, content)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &SetAvatarOutput{Body: newAvatarResponse(avatar)}, nil
}

func (h *avatarHandlers) get(ctx context.Context, _ *GetAvatarInput) (*GetAvatarOutput, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	meta, plain, err := h.files.Avatar(ctx, sess.UserID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &GetAvatarOutput{
		ContentType:   meta.DetectedType,
		ContentLength: strconv.FormatInt(int64(len(plain)), 10),
		Body:          plain,
	}, nil
}

func (h *avatarHandlers) delete(ctx context.Context, _ *DeleteAvatarInput) (*struct{}, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	if err := h.files.DeleteAvatar(ctx, sess.UserID); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}
