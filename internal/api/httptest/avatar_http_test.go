package httptest

import (
	"bytes"
	"encoding/json"
	"net/http"
	stdhttptest "net/http/httptest"
	"testing"
)

func TestAvatarUploadDownloadAndDeleteRoundTrip(t *testing.T) {
	f := NewAuthzFixture(t)
	png := minimalPNG()

	res := Do(t, f.Server.Handler(), authedUpload(t, f, "/v1/me/avatar", "face.png", png))
	if res.Code != http.StatusOK {
		t.Fatalf("upload status = %d; body %s", res.Code, res.Body.Bytes())
	}

	var created struct {
		DetectedType string `json:"detected_type"`
		SHA256       string `json:"sha256"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v (body %s)", err, res.Body.Bytes())
	}

	if created.DetectedType != "image/png" {
		t.Errorf("detected_type = %q, want image/png", created.DetectedType)
	}

	me := f.me(t)
	if me.ID == "" {
		t.Fatal("GET /v1/me returned an empty id")
	}

	var profile struct {
		Avatar *struct {
			DetectedType string `json:"detected_type"`
			SHA256       string `json:"sha256"`
		} `json:"avatar"`
	}
	f.call(t, http.MethodGet, "/v1/me", "").expect(t, http.StatusOK).decode(t, &profile)
	if profile.Avatar == nil || profile.Avatar.DetectedType != "image/png" {
		t.Errorf("GET /v1/me avatar = %+v, want png metadata", profile.Avatar)
	}

	dl := Do(t, f.Server.Handler(), Authed(t, http.MethodGet, "/v1/me/avatar", "", f.Token, ""))
	if dl.Code != http.StatusOK {
		t.Fatalf("download status = %d; body %s", dl.Code, dl.Body.Bytes())
	}

	if !bytes.Equal(dl.Body.Bytes(), png) {
		t.Error("downloaded bytes do not match the upload")
	}

	if ct := dl.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}

	f.call(t, http.MethodDelete, "/v1/me/avatar", "").expect(t, http.StatusNoContent)

	f.call(t, http.MethodGet, "/v1/me/avatar", "").expect(t, http.StatusNotFound)

	var after struct {
		Avatar *struct {
			DetectedType string `json:"detected_type"`
			SHA256       string `json:"sha256"`
		} `json:"avatar"`
	}
	f.call(t, http.MethodGet, "/v1/me", "").expect(t, http.StatusOK).decode(t, &after)
	if after.Avatar != nil {
		t.Errorf("GET /v1/me avatar after delete = %+v, want omitted", after.Avatar)
	}
}

func TestAvatarRejectsAPdf(t *testing.T) {
	f := NewAuthzFixture(t)

	res := Do(t, f.Server.Handler(), authedUpload(t, f, "/v1/me/avatar", "face.pdf", []byte("%PDF-1.4\n%")))
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body %s", res.Code, res.Body.Bytes())
	}

	body := DecodeProblem(t, res.Body.Bytes())
	if body.Code != "file_type_not_allowed" {
		t.Errorf("code = %q, want file_type_not_allowed", body.Code)
	}
}

func TestAvatarRejectsAnExecutable(t *testing.T) {
	f := NewAuthzFixture(t)

	res := Do(t, f.Server.Handler(), authedUpload(t, f, "/v1/me/avatar", "face.png", []byte("MZ\x90\x00not-a-png")))
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body %s", res.Code, res.Body.Bytes())
	}

	body := DecodeProblem(t, res.Body.Bytes())
	if body.Code != "file_executable" {
		t.Errorf("code = %q, want file_executable", body.Code)
	}
}

func TestAvatarRequiresAToken(t *testing.T) {
	f := NewAuthzFixture(t)

	req := stdhttptest.NewRequest(http.MethodPost, "/v1/me/avatar", nil)
	res := Do(t, f.Server.Handler(), req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body %s", res.Code, res.Body.Bytes())
	}
}
