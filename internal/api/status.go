package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/status"
	"github.com/j000k3r/probeglass/internal/store"
)

// handlePublicStatus answers GET /status/{token}.
//
// This is the one page the program serves to somebody who has no account. What
// keeps it safe is that it draws only what an administrator published, that it
// carries no target, option or credential, and that the URL is unguessable and
// revocable. It is a plain server-rendered page: no script runs on it, so there
// is no session for it to touch and nothing for a shared link to reach.
func (s *Server) handlePublicStatus(w http.ResponseWriter, r *http.Request) {
	given := r.PathValue("token")
	token, _ := s.store.StatusPageSettings(r.Context())
	// A page that is off and a wrong token answer the same thing. An outsider
	// should not be able to tell "this installation has such a page" from "this
	// token is stale", and the comparison is constant-time because the token is
	// the whole of the access control.
	if token == "" || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
		http.NotFound(w, r)
		return
	}

	page, err := status.Build(r.Context(), s.store, s.sched.Snapshots(), time.Now(), "/status/"+given)
	if err != nil {
		s.log.Error("build the public status page", "error", err)
		http.Error(w, "the status page could not be built", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// The token is in the URL. A search engine that indexed it would publish a
	// page meant for the people the link was given to, and a link followed from
	// this page would hand the token to whatever it points at — there is nothing
	// to follow today, but the header is what keeps that true tomorrow.
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	// No script, no image, no font, no frame. The styles are inline because the
	// page is one file by design; everything else is refused.
	h.Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; img-src 'self'; base-uri 'none'; form-action 'none'")

	if err := status.Render(w, page, r.Header.Get("Accept-Language")); err != nil {
		s.log.Error("render the public status page", "error", err)
	}
}

// handleStatusIcon answers GET /status/{token}/icon/{id}, the uploaded symbol of
// a published sensor.
//
// The dashboard's own icon route is behind a viewer session, and a public page
// has no session to present. Rather than making every upload public, this route
// lends the page's token to one image and only while that image belongs to a
// sensor the page actually shows. Built-in drawings and brand marks do not come
// through here — they are static assets.
func (s *Server) handleStatusIcon(w http.ResponseWriter, r *http.Request) {
	given := r.PathValue("token")
	token, _ := s.store.StatusPageSettings(r.Context())
	if token == "" || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 63)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	if !s.statusIconPublished(r.Context(), id) {
		http.NotFound(w, r)
		return
	}
	ic, data, err := s.store.GetIcon(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the icon")
		return
	}
	etag := `"icon-` + strconv.FormatInt(ic.ID, 10) + `"`
	h := w.Header()
	h.Set("Content-Type", ic.MediaType)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("ETag", etag)
	// The URL carries the token, so the cache entry is that one link's rather
	// than the installation's; a day is as long as a symbol needs.
	h.Set("Cache-Control", "private, max-age=86400")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(data)
}

// statusIconPublished reports whether an icon belongs to a sensor the current
// page configuration draws, either through the sensor or through an entry's
// override.
func (s *Server) statusIconPublished(ctx context.Context, id int64) bool {
	ref := "upload:" + strconv.FormatInt(id, 10)
	cfg := status.LoadConfig(ctx, s.store)
	onPage := map[int64]bool{}
	for _, section := range cfg.Sections {
		for _, entry := range section.Sensors {
			if entry.Icon == ref {
				return true
			}
			onPage[entry.ID] = true
		}
	}
	sensors, err := s.store.ListSensors(ctx)
	if err != nil {
		return false
	}
	for _, sensor := range sensors {
		if onPage[sensor.ID] && sensor.Icon == ref {
			return true
		}
	}
	return false
}

// statusPageResponse is the shape both the read and the save answer in, so the
// dashboard can redraw from either without a second request.
type statusPageResponse struct {
	Enabled bool          `json:"enabled"`
	Token   string        `json:"token"`
	Config  status.Config `json:"config"`
}

func (s *Server) statusPageState(r *http.Request) (statusPageResponse, error) {
	token, _ := s.store.StatusPageSettings(r.Context())
	return statusPageResponse{
		Enabled: token != "",
		Token:   token,
		Config:  status.LoadConfig(r.Context(), s.store),
	}, nil
}

// handleGetStatusPage answers GET /api/status-page: whether the page is on, its
// token, and which sensors it publishes.
func (s *Server) handleGetStatusPage(w http.ResponseWriter, r *http.Request) {
	state, err := s.statusPageState(r)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the status page settings")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleSaveStatusPage answers PUT /api/status-page with the selection the panel
// is showing. The token is not part of this: sharing and choosing who is on the
// page are two decisions, and rotating the link is a separate button so that a
// save cannot silently break every link somebody has.
func (s *Server) handleSaveStatusPage(w http.ResponseWriter, r *http.Request) {
	// The defaults are the starting point, so a body that leaves a switch out
	// keeps the behaviour the page had rather than silently turning it off. A
	// switch that is meant to be off says so.
	in := struct {
		Config status.Config `json:"config"`
	}{Config: status.DefaultConfig()}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	status.Normalise(&in.Config)

	sensors, err := s.store.ListSensors(r.Context())
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the sensors")
		return
	}
	known := make(map[int64]bool, len(sensors))
	for _, c := range sensors {
		known[c.ID] = true
	}
	for _, id := range in.Config.SensorIDs() {
		if !known[id] {
			writeError(w, http.StatusBadRequest, "sensor %d does not exist", id)
			return
		}
	}

	if err := status.SaveConfig(r.Context(), s.store, in.Config); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not save the status page")
		return
	}
	s.log.Info("public status page saved", "sensors", len(in.Config.SensorIDs()))
	state, err := s.statusPageState(r)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not read the status page back")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleRotateStatusToken makes a new share link, invalidating every old one.
func (s *Server) handleRotateStatusToken(w http.ResponseWriter, r *http.Request) {
	token, err := store.NewToken(24)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not generate a status page token")
		return
	}
	if err := s.store.SetSetting(r.Context(), model.SettingStatusToken, token); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not save the status page token")
		return
	}
	s.log.Info("public status page link rotated")
	state, err := s.statusPageState(r)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not read the status page back")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleDeleteStatusToken switches the page off: the URL answers 404 until a new
// link is generated. The selection is kept, so turning it back on restores the
// page somebody configured.
func (s *Server) handleDeleteStatusToken(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSetting(r.Context(), model.SettingStatusToken); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not remove the status page token")
		return
	}
	s.log.Info("public status page switched off")
	state, err := s.statusPageState(r)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not read the status page back")
		return
	}
	writeJSON(w, http.StatusOK, state)
}
