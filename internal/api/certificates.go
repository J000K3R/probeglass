package api

import (
	"net/http"
)

// handleCertificates answers GET /api/certificates.
//
// The inventory is one list and no parameters: it is small — one row per sensor
// that has ever completed a TLS handshake — and sorted where it is read, soonest
// to expire first. Every row already carries the sensor it belongs to, so the
// page can draw the whole table from one request and a click can open the sensor
// without a second lookup.
func (s *Server) handleCertificates(w http.ResponseWriter, r *http.Request) {
	certs, err := s.store.Certificates(r.Context())
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load certificates")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"certificates": certs})
}
