package api

import (
	"context"
	"net/http"
	"time"

	"github.com/j000k3r/probeglass/internal/catalog"
	"github.com/j000k3r/probeglass/internal/model"
	mosapi "github.com/j000k3r/probeglass/internal/mos"
)

// Discovering what a device has, after Check device has proved it can be
// reached. MOS is the first template with this: its API knows the disks, pools,
// containers, VMs and hardware sensors, and each becomes a suggestion the person
// can tick before anything is created.
func (s *Server) handleDiscoverSensorTemplate(w http.ResponseWriter, r *http.Request) {
	template, ok := catalog.Find(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "sensor template not found")
		return
	}
	if !template.Discover {
		writeJSON(w, http.StatusOK, map[string]any{"template": template.ID, "checks": []any{}})
		return
	}
	var in struct {
		Target  string        `json:"target"`
		Options model.Options `json:"options"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	if in.Target == "" {
		writeError(w, http.StatusBadRequest, "target is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	checks, notes, err := mosapi.Discover(ctx, in.Target, in.Options.Str("bearer_token", ""))
	if err != nil {
		writeError(w, http.StatusBadGateway, "%s", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"template": template.ID,
		"device":   map[string]string{"sys_descr": "MOS API at " + in.Target},
		"checks":   checks,
		"notes":    notes,
	})
}
