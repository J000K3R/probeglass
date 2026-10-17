package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
)

// Sites are the places sensors live. The handlers here are the same shape as the
// channel ones next door: a list anyone may read, writes only for administrators,
// and a delete that refuses while something still depends on the row.

// ---------------------------------------------------------------------------
// sites
// ---------------------------------------------------------------------------

// handleListSites answers GET /api/sites.
//
// Every site carries the number of sensors on it. The dashboard needs that for
// the headings in the tree and the wall, and the settings screen needs it to warn
// before a delete that would be refused — and both would otherwise have to fetch
// the sensors and count them, which is a second request for a number this one
// already knows.
func (s *Server) handleListSites(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sites, err := s.store.ListSites(ctx)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load sites")
		return
	}
	counts, err := s.store.SensorCountBySite(ctx)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not count the sensors per site")
		return
	}
	for i := range sites {
		sites[i].SensorCount = counts[sites[i].Name]
	}
	kinds := make([]string, 0, len(model.SiteKinds))
	for _, k := range model.SiteKinds {
		kinds = append(kinds, string(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": sites, "kinds": kinds})
}

// siteInput is the body of a site create or update.
type siteInput struct {
	Name string   `json:"name"`
	Kind string   `json:"kind"`
	Lat  *float64 `json:"lat"`
	Lon  *float64 `json:"lon"`
	Icon string   `json:"icon"`
}

func (in siteInput) toSite() model.Site {
	return model.Site{
		Name: in.Name,
		Kind: model.SiteKind(strings.ToLower(strings.TrimSpace(in.Kind))),
		Lat:  in.Lat,
		Lon:  in.Lon,
		Icon: strings.TrimSpace(in.Icon),
	}
}

// handleCreateSite answers POST /api/sites.
func (s *Server) handleCreateSite(w http.ResponseWriter, r *http.Request) {
	var in siteInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	site := in.toSite()
	if err := s.store.ValidateIconRef(r.Context(), site.Icon); err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	if err := s.store.CreateSite(r.Context(), &site); err != nil {
		s.writeSiteError(w, err, "could not create the site")
		return
	}
	s.log.Info("site created", "name", site.Name, "kind", string(site.Kind))
	writeJSON(w, http.StatusCreated, site)
}

// handleUpdateSite answers PUT /api/sites/{name}.
//
// The name in the path is the site being changed and the name in the body is what
// it should be called afterwards. They differ only when renaming, and a rename is
// a single UPDATE: the sensors follow through the foreign key's cascade, so
// nothing here has to walk them and nothing can miss one.
//
// A site the configuration file declares is refused outright, and the refusal
// covers the whole row rather than the name alone — the file owns the coordinates
// and the kind too. The dashboard already draws such a site with its buttons
// disabled, so the only callers that reach this are scripts and stale tabs; the
// answer they get is a 409 saying why, not a 200 that the next apply would quietly
// undo.
func (s *Server) handleUpdateSite(w http.ResponseWriter, r *http.Request) {
	oldName := r.PathValue("name")
	var in siteInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = oldName
	}
	site := in.toSite()
	ctx := r.Context()
	if err := s.store.ValidateIconRef(ctx, site.Icon); err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	if err := s.store.UpdateSite(ctx, oldName, &site); err != nil {
		s.writeSiteError(w, err, "could not update the site")
		return
	}
	// The body of a site always carries how many sensors are on it, and this is
	// the one route where that number cannot be known in advance: the sensors
	// arrive at the row through the rename, so it is counted here rather than
	// guessed. Leaving it at the zero value would answer a rename of a busy site
	// with "0 sensors" — true of nothing.
	n, err := s.store.CountSensorsAtSite(ctx, site.Name)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not count the sensors at the site")
		return
	}
	site.SensorCount = n
	s.log.Info("site updated", "was", oldName, "name", site.Name)
	writeJSON(w, http.StatusOK, site)
}

// handleDeleteSite answers DELETE /api/sites/{name}.
//
// A site that still has sensors on it is refused unless the caller passes
// ?unassign=1, and the refusal carries the count so that whoever reads it can
// decide. Deleting the site and quietly orphaning four sensors would be a much
// worse answer than saying no: the tree would simply lose a branch nobody
// remembered asking to remove.
func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	unassign := r.URL.Query().Get("unassign") == "1"

	err := s.store.DeleteSite(r.Context(), name, unassign)
	var inUse *store.SiteInUseError
	if errors.As(err, &inUse) {
		writeError(w, http.StatusConflict,
			"site %q still has %d sensor(s): move them to another site, or "+
				"delete it with unassign=1 to leave them without one",
			inUse.Name, inUse.Count)
		return
	}
	if err != nil {
		s.writeSiteError(w, err, "could not delete the site")
		return
	}
	s.log.Info("site deleted", "name", name, "unassigned", unassign)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name, "unassigned": unassign})
}

// writeSiteError maps the ways a site write can fail onto status codes.
//
// A validation failure and a mistyped name are both the caller's mistake and are
// answered as such. What is worth being careful about is not collapsing them into
// a 500: "a site needs a name" is not the server falling over, and reporting it as
// one sends whoever reads the log looking for a bug that is not there.
func (s *Server) writeSiteError(w http.ResponseWriter, err error, msg string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "site not found")
	case isValidationError(err):
		writeError(w, http.StatusBadRequest, "%s", err.Error())
	case isManagedSiteError(err):
		writeError(w, http.StatusConflict, "%s", err.Error())
	default:
		s.fail(w, err, http.StatusInternalServerError, "%s", msg)
	}
}

// isValidationError recognises the refusals the store produces for input it will
// not store. They are matched on text because the store speaks in sentences meant
// for a person — "latitude 91 is outside -90..90" — and giving each one a type so
// that a handler could switch on it would buy nothing a reader of the message
// does not already get.
//
// Every sentence the store refuses input with belongs in this list, which is why
// the icon and group ones are here beside the site ones: what the list decides is
// the difference between a 400 that says what to type instead and a 500 that sends
// whoever reads the log looking for a bug that is not there.
func isValidationError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{
		"a site needs a name",
		"may be at most 64 characters",
		"may not contain",
		"unknown site kind",
		"is outside",
		"both coordinates or neither",
		"already exists",
		// icons and groups
		"a group needs a name",
		"is not a colour",
		"is not an icon",
		"there is no built-in icon",
		"there is no uploaded icon",
		"an icon may be at most",
		"an icon has to be",
		"an icon name may be at most",
		"the image could not be read",
		"the file is empty",
		"the image has no size",
		"images of type",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// isManagedSiteError reports a row that the configuration file owns.
func isManagedSiteError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "declared in the configuration file")
}

// ---------------------------------------------------------------------------
// the map
// ---------------------------------------------------------------------------

// handleUpdateMap answers PUT /api/map.
//
// It sets where the map's tiles come from. That is an installation-wide decision
// rather than a preference, because it decides which host in the world every
// browser looking at this dashboard talks to — so it is an administrator's to
// make, and the CSP is refreshed with it in the same breath. Forgetting the
// second half would leave the tiles blocked by a policy that had just been told
// about them, and the symptom would be a map that is simply blank.
//
// An empty address is the off switch, not a request for the default. The default is
// empty anyway, and the map draws its own coastlines, but the distinction matters to
// the person clearing the field: they are saying they want nothing fetched, and
// that has to be what happens rather than a silent return to somebody else's server.
func (s *Server) handleUpdateMap(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TileURL     string  `json:"tile_url"`
		Attribution string  `json:"attribution"`
		Borders     *string `json:"borders"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}

	tileURL := strings.TrimSpace(in.TileURL)
	// validateTileURL is about the placeholders and the scheme, all of which are
	// about a fetch that is going to happen. With an empty field none is.
	if tileURL != "" {
		if err := validateTileURL(tileURL); err != nil {
			writeError(w, http.StatusBadRequest, "%s", err.Error())
			return
		}
	}

	// A pointer, so that leaving the field out keeps the setting rather than
	// clearing it. That is not only about the switch: the tile fields have always
	// been sent together with it, and a client that predates the borders — or a
	// script that only wanted to set a tile address — must not turn the layer off by
	// saying nothing about it.
	var borders string
	if in.Borders != nil {
		borders = strings.TrimSpace(*in.Borders)
		if borders != "on" && borders != "off" {
			writeError(w, http.StatusBadRequest,
				"borders is %q; it is \"on\" or \"off\"", borders)
			return
		}
	} else {
		borders = s.store.Setting(r.Context(), model.SettingMapBorders, model.DefaultMapBorders)
	}

	// No fallback either. The credit line exists to satisfy a tile server's terms,
	// and the only one whose terms this project has ever had to satisfy is
	// OpenStreetMap's — whose tiles are no longer being fetched, and whose credit
	// would therefore be a name attached to somebody else's map. attributionMarkup
	// in the browser still supplies it when a tile address really does point there.
	attribution := strings.TrimSpace(in.Attribution)

	ctx := r.Context()
	if err := s.store.SetSetting(ctx, model.SettingTileURL, tileURL); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not save the tile URL")
		return
	}
	if err := s.store.SetSetting(ctx, model.SettingTileAttribution, attribution); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not save the attribution")
		return
	}
	if err := s.store.SetSetting(ctx, model.SettingMapBorders, borders); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not save the map borders setting")
		return
	}
	s.refreshTileOrigin(ctx)
	s.log.Info("map tiles configured", "tile_origin", s.TileOrigin())
	writeJSON(w, http.StatusOK, map[string]any{
		"tile_url":    tileURL,
		"attribution": attribution,
		"borders":     borders,
		"origin":      s.TileOrigin(),
	})
}

// validateTileURL refuses a template that cannot work.
//
// Every one of these mistakes produces the same symptom — an empty map — and none
// of them produces an error anywhere a user would see it, because a tile that
// cannot be fetched is a broken image, which browsers report only in a console
// nobody opens. So the check happens here, where it can still be a message next
// to the field that is wrong.
func validateTileURL(tileURL string) error {
	u, err := url.Parse(tileURL)
	if err != nil {
		return fmt.Errorf("the tile URL cannot be parsed: %w", err)
	}
	if u.Host == "" {
		return errors.New("the tile URL needs a host")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("the tile URL has to start with http:// or https://")
	}
	// The placeholders are substituted before the request is made, so a template
	// missing one produces the same URL for every tile there is: one tile,
	// stretched over the whole map.
	for _, want := range []string{"{z}", "{x}", "{y}"} {
		if !strings.Contains(tileURL, want) {
			return fmt.Errorf("the tile URL needs %s: without it every tile "+
				"requests the same address", want)
		}
	}
	return nil
}
