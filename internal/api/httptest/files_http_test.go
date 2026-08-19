package httptest

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	stdhttptest "net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
)

func TestUploadingAFileNeedsThePermission(t *testing.T) {
	f := NewAuthzFixture(t)

	res := Do(t, f.Server.Handler(), authedUpload(t, f, f.orgPath("/files"), "shot.png", minimalPNG()))
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body %s", res.Code, res.Body.Bytes())
	}

	body := DecodeProblem(t, res.Body.Bytes())
	if body.RequiredPermission != string(authz.PermFilesCreate) {
		t.Errorf("required_permission = %q, want %q", body.RequiredPermission, authz.PermFilesCreate)
	}
}

func TestUploadDownloadAndDeleteRoundTrip(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)
	png := minimalPNG()

	res := Do(t, f.Server.Handler(), authedUpload(t, f, f.orgPath("/files"), "shot.png", png))
	if res.Code != http.StatusOK {
		t.Fatalf("upload status = %d; body %s", res.Code, res.Body.Bytes())
	}

	var created struct {
		ID           uuid.UUID `json:"id"`
		DetectedType string    `json:"detected_type"`
		SHA256       string    `json:"sha256"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v (body %s)", err, res.Body.Bytes())
	}

	if created.DetectedType != "image/png" {
		t.Errorf("detected_type = %q, want image/png", created.DetectedType)
	}

	if created.SHA256 == "" {
		t.Error("sha256 is empty")
	}

	dl := Do(t, f.Server.Handler(),
		Authed(t, http.MethodGet, f.orgPath("/files/"+created.ID.String()+"/content"), "", f.Token, ""))
	if dl.Code != http.StatusOK {
		t.Fatalf("download status = %d; body %s", dl.Code, dl.Body.Bytes())
	}

	if !bytes.Equal(dl.Body.Bytes(), png) {
		t.Error("downloaded bytes do not match the upload")
	}

	if ct := dl.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}

	f.call(t, http.MethodDelete, f.orgPath("/files/"+created.ID.String()), "").
		expect(t, http.StatusNoContent)

	f.call(t, http.MethodGet, f.orgPath("/files/"+created.ID.String()), "").
		expect(t, http.StatusNotFound)
}

func TestUploadRejectsAnExecutableNamedAsADocument(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	res := Do(t, f.Server.Handler(), authedUpload(t, f, f.orgPath("/files"), "innocent.pdf", []byte("MZ\x90\x00not-a-pdf")))
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body %s", res.Code, res.Body.Bytes())
	}

	body := DecodeProblem(t, res.Body.Bytes())
	if body.Code != "file_executable" {
		t.Errorf("code = %q, want file_executable", body.Code)
	}
}

func TestAFileFromAnotherOrganizationIsNotFound(t *testing.T) {
	owner := NewAuthzFixture(t, authz.RoleOwner)
	png := minimalPNG()

	res := Do(t, owner.Server.Handler(), authedUpload(t, owner, owner.orgPath("/files"), "shot.png", png))
	if res.Code != http.StatusOK {
		t.Fatalf("upload status = %d; body %s", res.Code, res.Body.Bytes())
	}

	var created struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	other := NewAuthzFixture(t, authz.RoleOwner)
	other.call(t, http.MethodGet, other.orgPath("/files/"+created.ID.String()), "").
		expect(t, http.StatusNotFound)
}

func authedUpload(t *testing.T, f *AuthzFixture, path, filename string, content []byte) *http.Request {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}

	if _, err := part.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := stdhttptest.NewRequest(http.MethodPost, path, bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+f.Token)

	return req
}

func minimalPNG() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
}
