package api

import (
	"net/http"

	"github.com/j000k3r/probeglass/internal/config"
)

// handleExportConfig answers GET /api/config/export with a probeglass.yaml
// describing the installation.
//
// It reads the store directly rather than going through the JSON handlers: those
// mask channel secrets, and a configuration file whose tokens were replaced by a
// marker would apply and then alert nowhere. Accounts, incidents and history are
// not in the document; this is the configuration, not the backup.
//
// Example data is left out unless ?examples=1 is asked for. It is invented to
// make a first dashboard look alive, and turning it into managed sensors by
// exporting it is not what "write my setup to a file" means.
func (s *Server) handleExportConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sites, err := s.store.ListSites(ctx)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the sites")
		return
	}
	channels, err := s.store.ListChannels(ctx)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the channels")
		return
	}
	sensors, err := s.store.ListSensors(ctx)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the sensors")
		return
	}
	includeExamples := r.URL.Query().Get("examples") == "1"
	body, err := config.Export(sites, channels, sensors, includeExamples)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not render the configuration")
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="probeglass.yaml"`)
	w.WriteHeader(http.StatusOK)
	// The body is YAML, and the content type says so; the XSS analysis assumes an
	// HTML response and has nothing to say about a document a browser downloads.
	_, _ = w.Write(body) //nolint:gosec // G705: YAML document, not HTML

}
