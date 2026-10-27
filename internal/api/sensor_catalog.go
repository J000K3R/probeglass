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

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// An SNMP template is checked by asking the device who it is. An application
	// template is checked by running its first sensor: a web page or a port
	// either answers with these settings or it does not.
	if !template.HasSNMP() {
		check := template.Checks[0]
		options := model.Options{}
		for k, v := range check.Options {
			options[k] = v
		}
		for k, v := range in.Options {
			options[k] = v
		}
		probe := model.Sensor{
			Name: "device check", Type: check.Type,
			Target:   catalog.ResolveTarget(check.Target, in.Target),
			Interval: check.Interval, Timeout: 10, Retries: 1, Options: options,
		}
		if probe.Interval < 5 {
			probe.Interval = 60
		}
		if err := sensor.Validate(probe); err != nil {
			writeError(w, http.StatusBadRequest, "%s", err.Error())
			return
		}
		out := sensor.Test(ctx, probe)
		message := out.Message
		if message == "" {
			if out.Success {
				message = "the check passed"
			} else {
				message = "the check failed"
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"template": template.ID,
			"device":   map[string]string{"sys_descr": message},
			"result": map[string]any{
				"success": out.Success, "message": message,
			},
		})
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
