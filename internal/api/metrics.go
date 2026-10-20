package api

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
)

// The Prometheus endpoint and the read-only token that guards it.
//
// The token is not the API key. A scraper is a second program that reads the
// installation's numbers on a schedule, and handing it an administrator's key
// would give it every write route as well. A token of its own can do exactly one
// thing: read this page.

// handleMetrics answers GET /metrics in the Prometheus text format.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := s.store.Setting(ctx, model.SettingMetricsToken, "")
	if token == "" {
		writeError(w, http.StatusNotFound, "metrics are not enabled; generate a token in Settings → Monitoring → Metrics")
		return
	}
	if !bearerTokenMatches(r, token) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="ProbeGlass metrics"`)
		writeError(w, http.StatusUnauthorized, "a metrics token is required")
		return
	}

	sensors, err := s.store.ListSensors(ctx)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load sensors")
		return
	}
	views, err := s.statusViews(ctx, sensors)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not build the dashboard")
		return
	}

	var b strings.Builder
	b.WriteString("# HELP probeglass_sensor_up 1 when the sensor's last state is up, 0 otherwise.\n")
	b.WriteString("# TYPE probeglass_sensor_up gauge\n")
	for _, v := range views {
		up := 0
		if v.Status == model.StatusUp {
			up = 1
		}
		fmt.Fprintf(&b, "probeglass_sensor_up%s %d\n", metricLabels(v, ""), up)
	}

	b.WriteString("# HELP probeglass_sensor_status The sensor's current state, carried as a label and valued 1.\n")
	b.WriteString("# TYPE probeglass_sensor_status gauge\n")
	for _, v := range views {
		status := string(v.Status)
		if status == "" {
			status = string(model.StatusUnknown)
		}
		fmt.Fprintf(&b, "probeglass_sensor_status%s 1\n", metricLabels(v, status))
	}

	b.WriteString("# HELP probeglass_sensor_uptime_ratio Probe-weighted uptime over a window, 0 to 1.\n")
	b.WriteString("# TYPE probeglass_sensor_uptime_ratio gauge\n")
	for _, v := range views {
		writeWindowRatio(&b, v, "24h", v.Uptime24h)
		writeWindowRatio(&b, v, "7d", v.Uptime7d)
		writeWindowRatio(&b, v, "30d", v.Uptime30d)
	}

	b.WriteString("# HELP probeglass_sensor_latency_seconds The last probe's latency in seconds.\n")
	b.WriteString("# TYPE probeglass_sensor_latency_seconds gauge\n")
	for _, v := range views {
		fmt.Fprintf(&b, "probeglass_sensor_latency_seconds%s %s\n",
			metricLabels(v, ""), formatFloat(v.LatencyMS/1000))
	}

	b.WriteString("# HELP probeglass_sensor_open_incident 1 when the sensor has an incident open right now.\n")
	b.WriteString("# TYPE probeglass_sensor_open_incident gauge\n")
	open := 0
	for _, v := range views {
		value := 0
		if v.OpenIncident != nil {
			value = 1
			open++
		}
		fmt.Fprintf(&b, "probeglass_sensor_open_incident%s %d\n", metricLabels(v, ""), value)
	}

	b.WriteString("# HELP probeglass_incidents_open Incidents open across the installation.\n")
	b.WriteString("# TYPE probeglass_incidents_open gauge\n")
	fmt.Fprintf(&b, "probeglass_incidents_open %d\n", open)

	counts := map[model.Status]int{}
	for _, v := range views {
		counts[v.Status]++
	}
	b.WriteString("# HELP probeglass_sensors Sensors by current state.\n")
	b.WriteString("# TYPE probeglass_sensors gauge\n")
	for _, status := range []model.Status{
		model.StatusUp, model.StatusDown, model.StatusWarn,
		model.StatusPaused, model.StatusUnknown,
	} {
		fmt.Fprintf(&b, "probeglass_sensors{%s} %d\n", labelPair("status", string(status)), counts[status])
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String())) //nolint:gosec // G705: Prometheus text, not HTML
}

// metricLabels renders the label set shared by the per-sensor metrics. The
// state is only added for the info metric; target is deliberately absent, since
// it is an address and these labels are read by another program on the network.
func metricLabels(v model.StatusView, status string) string {
	labels := "{" + labelPair("sensor", v.Name) +
		"," + labelPair("group", v.Group) +
		"," + labelPair("site", v.Site) +
		"," + labelPair("type", string(v.Type))
	if status != "" {
		labels += "," + labelPair("status", status)
	}
	return labels + "}"
}

// labelPair writes one quoted label. The value is escaped once here; using %q on
// an already escaped value would double every backslash in it.
func labelPair(key, value string) string {
	return key + `="` + labelEscape(value) + `"`
}

// labelEscape escapes what the Prometheus text format requires inside a quoted
// label value.
func labelEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func writeWindowRatio(b *strings.Builder, v model.StatusView, window string, percent float64) {
	fmt.Fprintf(b, "probeglass_sensor_uptime_ratio{%s,%s} %s\n",
		labelPair("sensor", v.Name), labelPair("window", window), formatFloat(percent/100))
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// bearerTokenMatches checks the Authorization header, and accepts X-API-Key as
// a second spelling for scrapers that cannot set one.
func bearerTokenMatches(r *http.Request, token string) bool {
	given := ""
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		if fields := strings.Fields(auth); len(fields) == 2 && strings.EqualFold(fields[0], "bearer") {
			given = fields[1]
		}
	}
	if given == "" {
		given = strings.TrimSpace(r.Header.Get("X-API-Key"))
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(token)) == 1
}

// handleGetMetricsToken tells an administrator whether metrics are enabled and
// what the token is. It is admin-only like the routes that change it.
func (s *Server) handleGetMetricsToken(w http.ResponseWriter, r *http.Request) {
	token := s.store.Setting(r.Context(), model.SettingMetricsToken, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": token != "",
		"token":   token,
	})
}

// handleRotateMetricsToken makes a new token, replacing any old one. A scraper
// holding the old value starts getting 401 at its next scrape.
func (s *Server) handleRotateMetricsToken(w http.ResponseWriter, r *http.Request) {
	token, err := store.NewToken(24)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not generate a metrics token")
		return
	}
	if err := s.store.SetSetting(r.Context(), model.SettingMetricsToken, token); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not save the metrics token")
		return
	}
	s.log.Info("metrics token rotated")
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "token": token})
}

// handleDeleteMetricsToken switches the endpoint off: the token is removed and
// /metrics answers 404 until a new one is generated.
func (s *Server) handleDeleteMetricsToken(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSetting(r.Context(), model.SettingMetricsToken); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not remove the metrics token")
		return
	}
	s.log.Info("metrics token removed")
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
}
