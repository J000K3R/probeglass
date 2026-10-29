package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/event"
	"github.com/j000k3r/probeglass/internal/icon"
	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
)

// Icons: the drawings that ship inside the binary, the images somebody uploaded,
// and the groups that wear them. The handlers are the shape the site ones next
// door are — a list anyone may read, writes only for administrators, and a delete
// that refuses while something still depends on the row.

// ---------------------------------------------------------------------------
// uploaded icons
// ---------------------------------------------------------------------------

// handleListIcons answers GET /api/icons.
//
// It answers with four things and not one, because the picker needs all of them to
// be a picker: the uploaded images, the names of the built-in drawings, and the two
// limits a client should not have to guess at — how large an image may be and how
// large its sides may be. Guessing them would produce a file picker that accepts a
// photograph and an upload that is refused, which is a worse way to find out.
//
// The built-in names come from the server rather than from a list in the browser so
// that there is one answer to "what can be chosen", and so that a drawing added to
// the binary is offered without a second edit in JavaScript.
func (s *Server) handleListIcons(w http.ResponseWriter, r *http.Request) {
	icons, err := s.store.ListIcons(r.Context())
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the icons")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"icons":       icons,
		"builtin":     icon.Names,
		"media_types": icon.MediaTypes,
		"max_bytes":   icon.MaxBytes,
		"max_side":    icon.MaxSide,
	})
}

// handleIconBytes answers GET /api/icons/{id} with the image itself.
//
// It is the one route here that answers with something other than JSON, and it is
// what an `<img src>` in the dashboard points at. The id is an AUTOINCREMENT row, so
// a number is never handed to a second image: what this URL returned once, it
// returns for the life of the database, which is why the answer may be cached
// without asking again and why the validator is the row's id and not its contents.
//
// It is behind the same authentication as everything else under /api. An icon is not
// a secret, but this dashboard has no public surface at all, and a second policy for
// images would be one more thing to get right for nothing.
func (s *Server) handleIconBytes(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	ic, data, err := s.store.GetIcon(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "icon not found")
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
	// Private: this is behind a session cookie, and a shared cache holding it would
	// be holding a copy of somebody's dashboard for whoever asks next.
	h.Set("Cache-Control", "private, max-age=86400")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if _, err := w.Write(data); err != nil {
		s.log.Error("writing an icon", "id", id, "error", err)
	}
}

// uploadSlack is how much room beyond the image itself an upload may take, for the
// multipart framing and the two form fields beside the file. It is deliberately
// small and deliberately not the limit that matters: internal/icon refuses what is
// too large with a sentence that says so, and this is only the hard stop that keeps
// a request from being read into memory at all. The friendly refusal has to be able
// to happen, which is why this is the larger of the two.
const uploadSlack = 8 << 10

// handleUploadIcon answers POST /api/icons.
//
// The body is multipart form data with one field called `file` and an optional
// `name` to call it something other than its filename. Multipart rather than a JSON
// body with base64 in it, because this is the one request in the API that carries a
// file, and the browser can build the body for it from a file input without the
// dashboard encoding anything by hand.
//
// Nothing about the request is trusted to say what the image is. The filename and
// the Content-Type are both chosen by whoever is uploading, so the media type and
// the dimensions come back from reading the bytes — see icon.Decode, which is also
// what refuses an SVG and a file that is not an image at all.
func (s *Server) handleUploadIcon(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// The cap is a little over the real one so that the readable refusal below can
	// happen; a body that busts this one is not an icon by any measure.
	r.Body = http.MaxBytesReader(w, r.Body, int64(icon.MaxBytes)+uploadSlack)
	if err := r.ParseMultipartForm(int64(icon.MaxBytes) + uploadSlack); err != nil {
		writeError(w, http.StatusBadRequest, "the upload could not be read: %s", parseErrText(err))
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "the upload has no file in it")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(icon.MaxBytes)+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "the upload could not be read: %s", err.Error())
		return
	}

	ic := model.Icon{Name: strings.TrimSpace(r.FormValue("name"))}
	if ic.Name == "" && header != nil {
		// The filename, without its extension: "fritzbox.png" is a better first name
		// than nothing, and the extension is what was just used to decide nothing at
		// all about the file.
		ic.Name = strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename))
	}
	if err := s.store.CreateIcon(ctx, &ic, data); err != nil {
		if isValidationError(err) {
			writeError(w, http.StatusBadRequest, "%s", err.Error())
			return
		}
		s.fail(w, err, http.StatusInternalServerError, "could not store the icon")
		return
	}
	s.log.Info("icon uploaded", "id", ic.ID, "media_type", ic.MediaType,
		"width", ic.Width, "height", ic.Height, "bytes", ic.Size)
	writeJSON(w, http.StatusCreated, ic)
}

// handleDeleteIcon answers DELETE /api/icons/{id}.
//
// An image something still wears is refused with the counts of what wears it, and
// `?clear=1` is the way to say that was understood: the sensors, sites and groups
// that pointed at it are emptied in the same transaction and the image goes. The
// shape is the site delete's, and for the same reason — "four sensors still use
// this" is the answer that lets somebody decide, and silently leaving them with a
// symbol that draws nothing is not.
func (s *Server) handleDeleteIcon(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	clear := r.URL.Query().Get("clear") == "1"
	err = s.store.DeleteIcon(r.Context(), id, clear)
	var inUse *store.IconInUseError
	if errors.As(err, &inUse) {
		writeError(w, http.StatusConflict,
			"this icon is still used by %d sensor(s), %d site(s) and %d group(s): "+
				"take it off them first, or delete it with clear=1 to empty those fields",
			inUse.Sensors, inUse.Sites, inUse.Groups)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "icon not found")
		return
	}
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not delete the icon")
		return
	}
	s.log.Info("icon deleted", "id", id, "cleared", clear)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id, "cleared": clear})
}

// ---------------------------------------------------------------------------
// the online catalogue
// ---------------------------------------------------------------------------

// catalogueSource is the site the online search looks at, named in the answer so
// that the dashboard can credit it without a second place to keep in step.
const catalogueSource = "dashboardicons.com"

// searchLimit is how many results one search returns. A person choosing a symbol
// does not read more than this, and the grid that draws them does not want to.
const searchLimit = 40

// handleSearchIcons answers GET /api/icons/search?q=.
//
// The answer is a result list with a flag rather than an error status when the
// catalogue could not be reached: "no network" is not a fault in the request, and
// the dashboard shows it as a sentence beside the search field rather than as a
// toast that says something went wrong. The flag is the whole of what separates
// the two cases — an empty list with offline=false is "nothing matched", which is
// a different sentence.
//
// Every call revalidates the online index, so an icon the project added since this
// process started is found by the search that is looking for it.
func (s *Server) handleSearchIcons(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 64 {
		query = query[:64]
	}
	results, err := s.catalogue.Search(r.Context(), query, searchLimit)
	if err == nil {
		if results == nil {
			results = []icon.CatalogueIcon{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"source":  catalogueSource,
			"offline": false,
			"results": results,
		})
		return
	}
	if errors.Is(err, icon.ErrCatalogueUnavailable) {
		s.log.Info("online icon search unavailable", "error", err)
		writeJSON(w, http.StatusOK, map[string]any{
			"source":  catalogueSource,
			"offline": true,
			"results": []icon.CatalogueIcon{},
		})
		return
	}
	s.fail(w, err, http.StatusInternalServerError, "could not search the icon catalogue")
}

// handleIconPreview answers GET /api/icons/preview/{name} with the online picture,
// without storing it.
//
// It exists so that the search can show what it found before anything is imported,
// and it answers from this server rather than sending the browser to the CDN for
// two reasons. The content policy allows images from this origin and from nowhere
// else, and a search field that only works after widening that policy would be a
// third party deciding what this dashboard may load. And the bytes go through
// icon.Fit and icon.Decode before they are served, so a preview is always something
// the import behind it would also accept — a picture that cannot be stored is not
// shown as though it could be.
//
// Fit rather than Decode because the catalogue's pictures are not all the size this
// program draws: a wide logo is 623×512, and refusing to show it would leave a
// broken picture where the search result is.
func (s *Server) handleIconPreview(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	data, err := s.catalogue.Fetch(r.Context(), name)
	switch {
	case errors.Is(err, icon.ErrBadIconName):
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	case errors.Is(err, icon.ErrCatalogueUnavailable):
		writeError(w, http.StatusBadGateway, "the online icon catalogue could not be reached")
		return
	case err != nil:
		writeError(w, http.StatusNotFound, "%s", err.Error())
		return
	}
	fitted, info, err := icon.Fit(data)
	if err != nil {
		writeError(w, http.StatusBadGateway, "the catalogue's picture is not an image this dashboard can keep: %s", err.Error())
		return
	}
	h := w.Header()
	h.Set("Content-Type", info.MediaType)
	h.Set("Content-Length", strconv.Itoa(len(fitted)))
	h.Set("ETag", `"preview-`+name+`"`)
	h.Set("Cache-Control", "private, max-age=86400")
	if _, err := w.Write(fitted); err != nil {
		s.log.Error("writing an icon preview", "name", name, "error", err)
	}
}

// importInput is the body of an import: the name of an icon from the catalogue.
type importInput struct {
	Name string `json:"name"`
}

// handleImportIcon answers POST /api/icons/import and stores the online picture as
// an ordinary uploaded icon.
//
// This is the whole of what "taking it from dashboardicons" means here: one fetch,
// one row in the icons table, and from then on it is a picture in this
// installation's database that draws without any network at all. The alternative —
// drawing it from the CDN — would put a third party in the path of every page load
// and take the image out of the one file a backup copies.
//
// Fit rather than Decode because the catalogue is not written to this program's
// limits: a wide logo is 623×512, and refusing it would mean the search offered a
// symbol that could not be taken. The picture is reduced to what the dashboard
// draws; a file somebody uploaded is still held to the limits as they are.
func (s *Server) handleImportIcon(w http.ResponseWriter, r *http.Request) {
	var in importInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "the request names no icon")
		return
	}
	ctx := r.Context()
	data, err := s.catalogue.Fetch(ctx, name)
	switch {
	case errors.Is(err, icon.ErrBadIconName):
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	case errors.Is(err, icon.ErrCatalogueUnavailable):
		writeError(w, http.StatusBadGateway, "the online icon catalogue could not be reached")
		return
	case err != nil:
		writeError(w, http.StatusNotFound, "%s", err.Error())
		return
	}
	fitted, _, err := icon.Fit(data)
	if err != nil {
		writeError(w, http.StatusBadRequest,
			"the catalogue's picture is not an image this dashboard can keep: %s", err.Error())
		return
	}
	ic := model.Icon{Name: name}
	if err := s.store.CreateIcon(ctx, &ic, fitted); err != nil {
		if isValidationError(err) {
			writeError(w, http.StatusBadRequest, "%s", err.Error())
			return
		}
		s.fail(w, err, http.StatusInternalServerError, "could not store the icon")
		return
	}
	s.log.Info("icon imported", "id", ic.ID, "name", name, "bytes", ic.Size)
	writeJSON(w, http.StatusCreated, ic)
}

// ---------------------------------------------------------------------------
// groups
// ---------------------------------------------------------------------------

// handleListGroups answers GET /api/groups.
//
// Every group a sensor names, plus every group somebody has said something about,
// each with the number of sensors in it and the list of colours a group may be
// given. The colours come from here rather than from a list in the browser for the
// reason the built-in icon names do: one answer to what may be chosen.
func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.ListGroups(r.Context())
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the groups")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"groups":  groups,
		"accents": model.Accents,
	})
}

// groupInput is the body of a group write. Every field is a pointer so that leaving
// one out keeps what is stored: the editor sends all three, a script that only
// wanted to give a group a colour sends one, and neither should have to know what
// the other fields currently are.
type groupInput struct {
	Icon      *string `json:"icon"`
	Colour    *string `json:"colour"`
	SortOrder *int    `json:"sort_order"`
}

// handleSetGroup answers PUT /api/groups/{name}.
//
// It creates the settings row if this is the first thing anybody has said about the
// group, which is why there is no POST: a group exists from the moment its name is
// typed on a sensor, so the first save is not a different act from the second.
func (s *Server) handleSetGroup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var in groupInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	ctx := r.Context()
	g, err := s.store.GetGroup(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		g = model.Group{Name: name}
	} else if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the group")
		return
	}
	if in.Icon != nil {
		g.Icon = *in.Icon
	}
	if in.Colour != nil {
		g.Colour = *in.Colour
	}
	if in.SortOrder != nil {
		g.SortOrder = *in.SortOrder
	}
	if err := s.store.SetGroup(ctx, &g); err != nil {
		if isValidationError(err) {
			writeError(w, http.StatusBadRequest, "%s", err.Error())
			return
		}
		s.fail(w, err, http.StatusInternalServerError, "could not save the group")
		return
	}
	s.log.Info("group settings saved", "name", g.Name, "icon", g.Icon, "colour", g.Colour)
	writeJSON(w, http.StatusOK, g)
}

// handleForgetGroup answers DELETE /api/groups/{name}.
//
// It removes what was said about a group and leaves the group itself alone: the
// sensors keep the word, the heading stays on the dashboard, and it simply has no
// icon and no colour of its own again. The answer says how many sensors are still in
// it, because "deleted" alone would read as though something had been removed from
// the monitoring, and nothing has.
func (s *Server) handleForgetGroup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	count, err := s.store.ForgetGroup(r.Context(), name)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not forget the group")
		return
	}
	s.log.Info("group settings forgotten", "name", name, "sensors", count)
	writeJSON(w, http.StatusOK, map[string]any{"forgotten": name, "sensors": count})
}

// handleDeleteGroup answers POST /api/groups/{name}/delete.
//
// A group is a word on its sensors, so "delete the group" is really a question
// about those sensors. mode "keep" removes only the group's settings (the old
// DELETE behaviour); "all" deletes every sensor carrying the word; "selected"
// deletes the ids given, each of which must belong to the group. Sensors declared
// in the configuration file are refused, because only the file may remove them.
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var in struct {
		Mode      string  `json:"mode"`
		SensorIDs []int64 `json:"sensor_ids"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	if in.Mode == "" {
		in.Mode = "keep"
	}
	all, err := s.store.ListSensors(r.Context())
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load sensors")
		return
	}
	inGroup := make(map[int64]model.Sensor)
	for _, c := range all {
		if c.Group == name {
			inGroup[c.ID] = c
		}
	}

	var deleting []model.Sensor
	switch in.Mode {
	case "keep":
	case "all":
		for _, c := range inGroup {
			deleting = append(deleting, c)
		}
	case "selected":
		if len(in.SensorIDs) == 0 {
			writeError(w, http.StatusBadRequest, "no sensors were selected")
			return
		}
		for _, id := range in.SensorIDs {
			c, ok := inGroup[id]
			if !ok {
				writeError(w, http.StatusBadRequest, "sensor %d is not in the group %q", id, name)
				return
			}
			deleting = append(deleting, c)
		}
	default:
		writeError(w, http.StatusBadRequest, "mode must be keep, selected or all, got %q", in.Mode)
		return
	}
	for _, c := range deleting {
		if c.Managed {
			writeError(w, http.StatusConflict,
				"sensor %q is declared in the configuration file and can only be removed there", c.Name)
			return
		}
	}
	for _, c := range deleting {
		if err := s.store.DeleteSensor(r.Context(), c.ID); err != nil {
			s.fail(w, err, statusForStoreError(err), "%s", err.Error())
			return
		}
		s.sched.Forget(c.ID)
		s.broker.Publish(event.Event{Kind: event.KindSensor, SensorID: c.ID, TS: time.Now().UTC()})
	}
	remaining, err := s.store.ForgetGroup(r.Context(), name)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not forget the group")
		return
	}
	if err := s.sched.Reconcile(r.Context()); err != nil {
		s.log.Error("reconcile after group delete", "error", err)
	}
	s.log.Info("group deleted", "name", name, "mode", in.Mode, "deleted", len(deleting), "remaining", remaining)
	writeJSON(w, http.StatusOK, map[string]any{
		"forgotten": name, "deleted": len(deleting), "sensors": remaining,
	})
}

// ---------------------------------------------------------------------------
// tags
// ---------------------------------------------------------------------------

// handleListTags answers GET /api/tags.
//
// Every tag a sensor carries, plus every tag somebody has said something about, each
// with the number of sensors carrying it and the colours a tag may be given. The
// shape is the group list's, because the editor is the same editor.
func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	tags, err := s.store.ListTags(r.Context())
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the tags")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tags":    tags,
		"accents": model.Accents,
	})
}

// tagInput is the body of a tag write. Every field is a pointer for the reason
// groupInput's are: leaving one out keeps what is stored.
type tagInput struct {
	Icon      *string `json:"icon"`
	Colour    *string `json:"colour"`
	SortOrder *int    `json:"sort_order"`
}

// handleSetTag answers PUT /api/tags/{name}.
//
// It creates the settings row if this is the first thing anybody has said about the
// tag, which is why there is no POST: a tag exists from the moment it is typed into
// a sensor's list, so the first save is not a different act from the second.
func (s *Server) handleSetTag(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var in tagInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	ctx := r.Context()
	t, err := s.store.GetTag(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		t = model.Tag{Name: name}
	} else if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the tag")
		return
	}
	if in.Icon != nil {
		t.Icon = *in.Icon
	}
	if in.Colour != nil {
		t.Colour = *in.Colour
	}
	if in.SortOrder != nil {
		t.SortOrder = *in.SortOrder
	}
	if err := s.store.SetTag(ctx, &t); err != nil {
		if isValidationError(err) {
			writeError(w, http.StatusBadRequest, "%s", err.Error())
			return
		}
		s.fail(w, err, http.StatusInternalServerError, "could not save the tag")
		return
	}
	s.log.Info("tag settings saved", "name", t.Name, "icon", t.Icon, "colour", t.Colour)
	writeJSON(w, http.StatusOK, t)
}

// handleForgetTag answers DELETE /api/tags/{name}.
//
// It removes what was said about a tag and leaves the word on the sensors, exactly
// as forgetting a group leaves the group standing. The answer says how many sensors
// still carry it.
func (s *Server) handleForgetTag(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	count, err := s.store.ForgetTag(r.Context(), name)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not forget the tag")
		return
	}
	s.log.Info("tag settings forgotten", "name", name, "sensors", count)
	writeJSON(w, http.StatusOK, map[string]any{"forgotten": name, "sensors": count})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// parseErrText says what was wrong with the multipart body in words that make
// sense to whoever sent it. The MaxBytesError is the one worth naming: its own
// message is about an HTTP body, and the sentence a person gets should be about
// their file.
func parseErrText(err error) string {
	return parseUploadErrText(err, icon.MaxBytes, "an icon")
}

// parseUploadErrText is the sentence for a multipart body over its cap. The noun
// is the caller's: "an icon", "a background image".
func parseUploadErrText(err error, maxBytes int, noun string) string {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return fmt.Sprintf("the file is larger than the %s %s may be", uploadSizeLimit(maxBytes), noun)
	}
	return err.Error()
}

// uploadSizeLimit writes a byte cap the way a person says it.
func uploadSizeLimit(n int) string {
	if n%(1<<20) == 0 && n >= 1<<20 {
		return fmt.Sprintf("%d MiB", n>>20)
	}
	if n%1024 == 0 && n >= 1024 {
		return fmt.Sprintf("%d KiB", n/1024)
	}
	return fmt.Sprintf("%d bytes", n)
}
