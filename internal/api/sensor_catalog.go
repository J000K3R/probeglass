package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/catalog"
	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/sensor"
)

// Checking a device before a template is applied.
//
// The endpoint makes one real SNMP request with the credentials the person typed
// into the template dialog. It is the answer to "will these sensors work at all",
// and it names the device in reply, so applying the wrong vendor's template is
// visible before a dozen sensors exist to point at the wrong box.

func (s *Server) handleCheckSensorTemplate(w http.ResponseWriter, r *http.Request) {
	template, ok := catalog.Find(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "sensor template not found")
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
	if strings.TrimSpace(in.Target) == "" {
		writeError(w, http.StatusBadRequest, "target is required")
		return
	}

	// Validate the target and the credentials the same way a saved sensor is
	// validated. The OID and assertion are only there to satisfy the SNMP shape;
	// the check itself asks for sysDescr.
	probe := model.Sensor{
		Name: "device check", Type: model.SensorSNMP, Target: in.Target,
		Interval: 60, Timeout: 10, Retries: 1, Options: in.Options,
	}
	if probe.Options == nil {
		probe.Options = model.Options{}
	}
	probe.Options["oid"] = "1.3.6.1.2.1.1.1.0"
	probe.Options["min"] = 0
	if err := sensor.Validate(probe); err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	device, err := sensor.DiscoverSNMP(ctx, in.Target, in.Options)
	if err != nil {
		writeError(w, http.StatusBadGateway, "%s", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"template": template.ID,
		"device":   device,
	})
}
