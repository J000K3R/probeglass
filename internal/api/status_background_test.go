package api

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/j000k3r/probeglass/internal/icon"
	"github.com/j000k3r/probeglass/internal/model"
)

// uploadStatusBackground posts one file the way the panel's file input does.
func (h *harness) uploadStatusBackground(filename string, data []byte) *http.Response {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		h.t.Fatalf("build the form: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		h.t.Fatalf("write the file part: %v", err)
	}
	if err := mw.Close(); err != nil {
		h.t.Fatalf("close the form: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, h.http.URL+"/api/status-background", &buf)
	if err != nil {
		h.t.Fatalf("build the request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", h.http.URL)
	if h.cookie != nil {
		req.AddCookie(h.cookie)
	}
	resp, err := h.http.Client().Do(req)
	if err != nil {
		h.t.Fatalf("upload background: %v", err)
	}
	return resp
}

// TestTheUploadedBackgroundWinsOverTheURL walks the whole feature once: upload,
// preview, the public page, a replacement, and the fallback after removal.
func TestTheUploadedBackgroundWinsOverTheURL(t *testing.T) {
	h := newHarness(t)
	h.setup()

	state := h.statusPage(t)
	if state.Background != nil {
		t.Fatalf("a fresh installation has a background: %+v", state.Background)
	}
	if state.Limits.MaxSide != icon.BackgroundMaxSide || state.Limits.MaxBytes != icon.BackgroundMaxBytes {
		t.Errorf("limits = %+v, want the background limits", state.Limits)
	}

	first := pngBytes(t, 3840, 2160)
	resp := h.uploadStatusBackground("photo.png", first)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var bg model.StatusBackground
	decode(t, resp, &bg)
	if bg.Width != 3840 || bg.Height != 2160 || bg.MediaType != "image/png" || bg.Digest == "" {
		t.Fatalf("stored background = %+v", bg)
	}
	if bg.Size != len(first) {
		t.Errorf("size = %d, want %d", bg.Size, len(first))
	}

	state = h.statusPage(t)
	if state.Background == nil || state.Background.Digest != bg.Digest {
		t.Fatalf("the panel does not see the upload: %+v", state.Background)
	}

	// The admin preview is the image itself.
	resp = h.do(http.MethodGet, "/api/status-background", nil, map[string]string{"If-None-Match": `"bg-` + bg.Digest + `"`})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("preview with the ETag: status = %d, want 304", resp.StatusCode)
	}
	preview := h.do(http.MethodGet, "/api/status-background", nil, nil)
	previewBytes, _ := io.ReadAll(preview.Body)
	preview.Body.Close()
	if preview.StatusCode != http.StatusOK || !bytes.Equal(previewBytes, first) {
		t.Fatalf("preview: status = %d, %d bytes, want the uploaded image", preview.StatusCode, len(previewBytes))
	}

	// A configured URL is the fallback; the upload is what the page draws.
	resp = h.do(http.MethodPut, "/api/status-page", map[string]any{
		"config": map[string]any{"background_url": "https://example.org/bg.jpg"},
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save page: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	resp = h.do(http.MethodPut, "/api/status-token", nil, nil)
	var pageState struct {
		Token string `json:"token"`
	}
	decode(t, resp, &pageState)

	page := h.publicPage(t, pageState.Token)
	wantURL := "/status/" + pageState.Token + "/background?v=" + bg.Digest
	if !strings.Contains(page, wantURL) {
		t.Errorf("the page does not draw %q", wantURL)
	}
	if strings.Contains(page, "https://example.org/bg.jpg") {
		t.Error("the page drew the URL although an upload exists")
	}

	bgResp := h.publicBackground(t, pageState.Token, "")
	if bgResp.StatusCode != http.StatusOK {
		t.Fatalf("public background: status = %d", bgResp.StatusCode)
	}
	if got := bgResp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("content type = %q", got)
	}
	if got := bgResp.Header.Get("ETag"); got != `"bg-`+bg.Digest+`"` {
		t.Errorf("etag = %q", got)
	}
	bgResp.Body.Close()
	if cached := h.publicBackground(t, pageState.Token, `"bg-`+bg.Digest+`"`); cached.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional background: status = %d, want 304", cached.StatusCode)
	}

	// A replacement changes the digest and therefore the URL the page names.
	second := pngBytes(t, 320, 200)
	resp = h.uploadStatusBackground("second.png", second)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("replace: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var replaced model.StatusBackground
	decode(t, resp, &replaced)
	if replaced.Digest == bg.Digest {
		t.Fatal("the replacement kept the old digest")
	}
	if !strings.Contains(h.publicPage(t, pageState.Token), "/background?v="+replaced.Digest) {
		t.Error("the page still names the old version")
	}

	// Removing it falls back to the URL, and the routes answer 404.
	resp = h.do(http.MethodDelete, "/api/status-background", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	if !strings.Contains(h.publicPage(t, pageState.Token), "https://example.org/bg.jpg") {
		t.Error("the URL did not come back after the upload was removed")
	}
	if got := h.publicBackground(t, pageState.Token, ""); got.StatusCode != http.StatusNotFound {
		t.Errorf("the removed background answered %d", got.StatusCode)
	}
	if resp := h.do(http.MethodGet, "/api/status-background", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the admin preview of a removed background answered %d", resp.StatusCode)
	}
	if resp := h.do(http.MethodDelete, "/api/status-background", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a second delete answered %d", resp.StatusCode)
	}
}

// TestTheStatusBackgroundRefusesWhatItCannotBe covers the limits and the auth.
func TestTheStatusBackgroundRefusesWhatItCannotBe(t *testing.T) {
	h := newHarness(t)
	h.setup()

	h.addUser(t, "viewer", "viewer-password", model.RoleViewer)
	viewerCookie := h.signIn("viewer", "viewer-password")
	resp := h.as(viewerCookie, func() *http.Response {
		return h.do(http.MethodGet, "/api/status-page", nil, nil)
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer reading the status page: status = %d, want 403", resp.StatusCode)
	}

	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"too wide", pngBytes(t, icon.BackgroundMaxSide+1, 1), "a background image may be at most 4096×4096 pixels"},
		{"not an image", []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"), "a background image has to be a PNG, a JPEG or a GIF"},
		{"too many bytes", bytes.Repeat([]byte("p"), icon.BackgroundMaxBytes+1), "a background image may be at most 12 MiB"},
	}
	for _, tc := range cases {
		resp := h.uploadStatusBackground("file.bin", tc.data)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.name, resp.StatusCode)
			continue
		}
		if body := readBody(t, resp); !strings.Contains(body, tc.want) {
			t.Errorf("%s: body = %s, want %q", tc.name, body, tc.want)
		}
	}

	// A body larger than the multipart cap is refused before a decoder sees it.
	resp = h.uploadStatusBackground("huge.bin", bytes.Repeat([]byte("p"), icon.BackgroundMaxBytes+statusBackgroundSlack+1))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an oversized body: status = %d, want 400", resp.StatusCode)
	} else if body := readBody(t, resp); !strings.Contains(body, "larger than the 12 MiB") {
		t.Errorf("an oversized body: body = %s", body)
	}

	if state := h.statusPage(t); state.Background != nil {
		t.Errorf("a refused upload wrote a row: %+v", state.Background)
	}
}

// statusPage reads GET /api/status-page as the signed-in administrator.
func (h *harness) statusPage(t *testing.T) statusPageResponse {
	t.Helper()
	resp := h.do(http.MethodGet, "/api/status-page", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status page: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var out statusPageResponse
	decode(t, resp, &out)
	return out
}

// publicPage fetches the page without a session; the token is the only credential.
func (h *harness) publicPage(t *testing.T, token string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.http.URL+"/status/"+token, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := h.http.Client().Do(req)
	if err != nil {
		t.Fatalf("public page: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

// publicBackground fetches the background without a session, optionally with an
// If-None-Match header.
func (h *harness) publicBackground(t *testing.T, token, ifNoneMatch string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.http.URL+"/status/"+token+"/background", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	resp, err := h.http.Client().Do(req)
	if err != nil {
		t.Fatalf("public background: %v", err)
	}
	return resp
}
