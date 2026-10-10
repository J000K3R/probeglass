package api

import (
	"net/http"
	"strings"

	"github.com/j000k3r/weatherglass/internal/model"
	"github.com/j000k3r/weatherglass/internal/store"
)

// handleSearch answers the box in the top bar.
//
// It is a viewer route because everything it reads is something a viewer may
// already read: the sensors, the places, the channels, the incidents. The one
// exception is the account list, which is an administrator's, and that is decided
// here rather than in the browser — a search that a viewer could widen by editing
// the query string would be a second way round an authorization rule, and the
// second way is always the one that gets forgotten.
//
// A query shorter than the minimum is not an error. Somebody typing a letter at a
// time is not doing anything wrong, and answering their first keystroke with a
// red toast would teach them to stop typing; the answer is simply an empty list.
//
// There is no `limit` parameter. There was one, and it was read with a ceiling of
// zero, so every value including a sensible one came out as "use the default": a
// knob that was wired to nothing. The store's own per-kind bound is the right one
// for the only caller there is — the dashboard fetches one page and says when there
// is more — and a query string is the wrong place to renegotiate it.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) > store.SearchMaxRunes {
		writeError(w, http.StatusBadRequest,
			"a search may be at most %d characters", store.SearchMaxRunes)
		return
	}

	p, _ := principalOf(r)
	result, err := s.store.Search(r.Context(), store.SearchOptions{
		Query:        query,
		IncludeUsers: p.Role().Rank() >= model.RoleAdmin.Rank(),
	})
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not search")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
