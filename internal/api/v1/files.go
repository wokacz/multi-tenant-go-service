package v1

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/files"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

const maxFileNameLength = files.MaxNameLength

const (
	_ = uint(files.MaxNameLength - maxFileNameLength)
	_ = uint(maxFileNameLength - files.MaxNameLength)
)

// FileResponse is the metadata of a stored file. The bytes themselves are at
// GET .../content; putting them here would force every listing to decrypt.
type FileResponse struct {
	ID           uuid.UUID `json:"id" format:"uuid" doc:"Unique identifier"`
	OriginalName string    `json:"original_name" doc:"Sanitized name supplied at upload"`
	DeclaredType string    `json:"declared_type,omitempty" doc:"Content-Type the client sent, if any"`
	DetectedType string    `json:"detected_type" doc:"Media type detected from magic bytes"`
	SizeBytes    int64     `json:"size_bytes" doc:"Plaintext size in bytes"`
	SHA256       string    `json:"sha256" doc:"SHA-256 of the plaintext, hex"`
	ScanStatus   string    `json:"scan_status" enum:"skipped,clean,unavailable" doc:"What the malware scanner concluded"`
	ScanEngine   string    `json:"scan_engine,omitempty" doc:"Scanner that produced scan_status"`
	UploadedBy   uuid.UUID `json:"uploaded_by" format:"uuid" doc:"Account that uploaded the file"`
	CreatedAt    time.Time `json:"created_at" doc:"When it was stored"`
}

func newFileResponse(f *ent.File) FileResponse {
	return FileResponse{
		ID:           f.ID,
		OriginalName: f.OriginalName,
		DeclaredType: f.DeclaredType,
		DetectedType: f.DetectedType,
		SizeBytes:    f.SizeBytes,
		SHA256:       f.Sha256,
		ScanStatus:   string(f.ScanStatus),
		ScanEngine:   f.ScanEngine,
		UploadedBy:   f.UploadedBy,
		CreatedAt:    f.CreatedAt,
	}
}

type ListFilesInput struct {
	OrgID  uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	Limit  int       `query:"limit" minimum:"1" maximum:"100" default:"100" doc:"How many to return"`
	Offset int       `query:"offset" minimum:"0" default:"0" doc:"How many to skip"`
}

type ListFilesOutput struct {
	Body struct {
		Files []FileResponse `json:"files"`
	}
}

type GetFileInput struct {
	OrgID  uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	FileID uuid.UUID `path:"fileID" format:"uuid" doc:"File id"`
}

type GetFileOutput struct {
	Body FileResponse
}

type UploadFileInput struct {
	OrgID   uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	RawBody huma.MultipartFormFiles[struct {
		File huma.FormFile `form:"file" required:"true" doc:"The file to store"`
	}]
}

type UploadFileOutput struct {
	Body FileResponse
}

type DownloadFileInput struct {
	OrgID  uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	FileID uuid.UUID `path:"fileID" format:"uuid" doc:"File id"`
}

type DownloadFileOutput struct {
	ContentType        string `header:"Content-Type"`
	ContentDisposition string `header:"Content-Disposition"`
	ContentLength      string `header:"Content-Length"`
	Body               []byte
}

type DeleteFileInput struct {
	OrgID  uuid.UUID `path:"orgID" format:"uuid" doc:"Organization id"`
	FileID uuid.UUID `path:"fileID" format:"uuid" doc:"File id"`
}

type fileHandlers struct {
	files *files.Service
}

func registerFiles(api huma.API, service *files.Service) {
	h := &fileHandlers{files: service}

	huma.Register(api, huma.Operation{
		OperationID: "list-files",
		Method:      http.MethodGet,
		Path:        Prefix + "/orgs/{orgID}/files",
		Summary:     "List stored files",
		Description: "Requires files.read. Returns metadata only; the bytes are at GET .../files/{fileID}/content. " +
			"A caller with no active membership gets 404 rather than 403.",
		Tags:     []string{"files"},
		Security: bearer(),
		Errors:   orgErrors(),
	}, h.list)

	huma.Register(api, huma.Operation{
		OperationID: "upload-file",
		Method:      http.MethodPost,
		Path:        Prefix + "/orgs/{orgID}/files",
		Summary:     "Upload a file",
		Description: "Requires files.create. The payload is sniffed from magic bytes, optionally scanned, " +
			"encrypted at rest, and stored. A Content-Type that disagrees with the bytes is refused.",
		Tags:     []string{"files"},
		Security: bearer(),
		Errors:   append(orgErrors(), http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity, http.StatusServiceUnavailable),
	}, h.upload)

	huma.Register(api, huma.Operation{
		OperationID: "get-file",
		Method:      http.MethodGet,
		Path:        Prefix + "/orgs/{orgID}/files/{fileID}",
		Summary:     "Read file metadata",
		Description: "Requires files.read.",
		Tags:        []string{"files"},
		Security:    bearer(),
		Errors:      orgErrors(),
	}, h.get)

	huma.Register(api, huma.Operation{
		OperationID: "download-file",
		Method:      http.MethodGet,
		Path:        Prefix + "/orgs/{orgID}/files/{fileID}/content",
		Summary:     "Download a file",
		Description: "Requires files.read. Decrypts the stored blob and returns it as an attachment.",
		Tags:        []string{"files"},
		Security:    bearer(),
		Errors:      orgErrors(),
		Responses: map[string]*huma.Response{
			"200": {
				Description: "Decrypted file",
				Content: map[string]*huma.MediaType{
					"application/octet-stream": {},
				},
			},
		},
	}, h.download)

	huma.Register(api, huma.Operation{
		OperationID: "delete-file",
		Method:      http.MethodDelete,
		Path:        Prefix + "/orgs/{orgID}/files/{fileID}",
		Summary:     "Delete a file",
		Description: "Requires files.delete. Removes the ciphertext and the metadata.",
		Tags:        []string{"files"},
		Security:    bearer(),
		Errors:      orgErrors(),
	}, h.delete)
}

func (h *fileHandlers) list(ctx context.Context, in *ListFilesInput) (*ListFilesOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := h.files.List(ctx, grant.OrganizationID(), in.Limit, in.Offset)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	out := &ListFilesOutput{}
	out.Body.Files = make([]FileResponse, 0, len(rows))
	for i := range rows {
		out.Body.Files = append(out.Body.Files, newFileResponse(rows[i]))
	}

	return out, nil
}

func (h *fileHandlers) get(ctx context.Context, in *GetFileInput) (*GetFileOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	row, err := h.files.File(ctx, grant.OrganizationID(), in.FileID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &GetFileOutput{Body: newFileResponse(row)}, nil
}

func (h *fileHandlers) upload(ctx context.Context, in *UploadFileInput) (*UploadFileOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
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

	row, err := h.files.Upload(ctx, grant.OrganizationID(), grant.Actor(), part.Filename, part.ContentType, content)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &UploadFileOutput{Body: newFileResponse(row)}, nil
}

func (h *fileHandlers) download(ctx context.Context, in *DownloadFileInput) (*DownloadFileOutput, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	row, plain, err := h.files.Content(ctx, grant.OrganizationID(), in.FileID)
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	return &DownloadFileOutput{
		ContentType:        row.DetectedType,
		ContentDisposition: contentDisposition(row.OriginalName),
		ContentLength:      strconv.FormatInt(int64(len(plain)), 10),
		Body:               plain,
	}, nil
}

func (h *fileHandlers) delete(ctx context.Context, in *DeleteFileInput) (*struct{}, error) {
	grant, err := grantFrom(ctx)
	if err != nil {
		return nil, err
	}

	if err := h.files.Delete(ctx, grant.OrganizationID(), in.FileID); err != nil {
		return nil, problem.Error(ctx, err)
	}

	return nil, nil
}

func contentDisposition(name string) string {
	escaped := strings.ReplaceAll(name, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)

	return fmt.Sprintf(`attachment; filename="%s"`, escaped)
}
