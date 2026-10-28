package api

import (
	"context"
	"net/http"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/scan"
)

// handleScanNetwork looks at a private IPv4 range and returns the hosts that
// answered with sensors to suggest. It is an administrator's action — it dials
// addresses the caller chose — and the scan package refuses anything that is not
// a private, loopback or link-local range of at most 256 addresses.
func (s *Server) handleScanNetwork(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CIDR string        `json:"cidr"`
		SNMP model.Options `json:"snmp"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	hosts, notes, err := scan.Scan(ctx, in.CIDR, in.SNMP)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": hosts, "notes": notes})
}
