package api

import (
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/j000k3r/probeglass/internal/icon"
	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
)

// statusBackgroundSlack is how much room the multipart framing gets above the
// image's own cap: boundaries and part headers are not the picture.
const statusBackgroundSlack = 64 << 10

// handleUploadStatusBackground answers POST /api/status-background.
//
// A separate route from PUT /api/status-page because the page is saved as JSON and
// a 4K photograph would not fit its 1 MiB body. The upload is the same multipart
// shape the icon picker already uses, with the larger background limits.
func (s *Server) handleUploadStatusBackground(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := icon.BackgroundLimits.MaxBytes
	r.Body = http.MaxBytesReader(w, r.Body, int64(limit)+statusBackgroundSlack)
	// A small memory budget: the file spills to a temporary file instead of being
	// held twice — once by the parser and once by the read below.
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "%s", parseUploadErrText(err, limit, "a background image"))
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "the upload has no file in it")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "the upload could not be read: %s", err.Error())
		return
	}

	var bg model.StatusBackground
	if err := s.store.SetStatusBackground(ctx, &bg, data); err != nil {
		if isValidationError(err) {
			writeError(w, http.StatusBadRequest, "%s", err.Error())
			return
		}
		s.fail(w, err, http.StatusInternalServerError, "could not store the background image")
		return
	}
	s.log.Info("status background uploaded",
		"media_type", bg.MediaType, "width", bg.Width, "height", bg.Height, "bytes", bg.Size)
	writeJSON(w, http.StatusCreated, bg)
}

// handleGetStatusBackground answers GET /api/status-background with the image
// itself, for the panel's preview. The public route needs the page's token; this
// one needs the session, and works while the page is switched off.
func (s *Server) handleGetStatusBackground(w http.ResponseWriter, r *http.Request) {
	bg, data, ok, err := s.store.StatusBackgroundBytes(r.Context())
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the background image")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "there is no uploaded background image")
		return
	}
	s.serveStatusBackground(w, r, bg, data, "private, no-cache")
}

// handleDeleteStatusBackground removes the uploaded image. The configured
// background_url, if any, is what the page shows again afterwards.
func (s *Server) handleDeleteStatusBackground(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteStatusBackground(r.Context()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "there is no uploaded background image")
			return
		}
		s.fail(w, err, http.StatusInternalServerError, "could not remove the background image")
		return
	}
	s.log.Info("status background removed")
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// handleStatusBackground answers GET /status/{token}/background: the page's
// photograph, lent the share token the way its sensors' symbols are.
func (s *Server) handleStatusBackground(w http.ResponseWriter, r *http.Request) {
	given := r.PathValue("token")
	token, _ := s.store.StatusPageSettings(r.Context())
	if token == "" || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
		http.NotFound(w, r)
		return
	}
	bg, data, ok, err := s.store.StatusBackgroundBytes(r.Context())
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the background image")
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.serveStatusBackground(w, r, bg, data, "private, max-age=86400")
}

// serveStatusBackground writes the image with the same cache contract as the
// icon routes: the digest as the ETag, a conditional request answered with 304.
func (s *Server) serveStatusBackground(w http.ResponseWriter, r *http.Request, bg model.StatusBackground, data []byte, cacheControl string) {
	etag := `"bg-` + bg.Version() + `"`
	h := w.Header()
	h.Set("Content-Type", bg.MediaType)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("ETag", etag)
	h.Set("Cache-Control", cacheControl)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(data)
}
