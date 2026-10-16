package api

import (
	"net/http"
	"testing"

	"github.com/j000k3r/probeglass/internal/model"
)

// The channel is where a build came from, and it is stamped by whichever workflow made
// it. It exists because a container cannot see the tag it was pulled by: an installation
// following `latest` and one following `dev` are identical from the inside — same
// version, same commit, same date whenever both follow the same push. Somebody running
// both, as the first person to ask for this was, has nothing else to tell them apart.

// TestTheChannelReachesBothReports checks the two places that answer "what is this".
//
// They are separate on purpose and were checked separately: `/api/version` is for
// anything outside the dashboard, and the settings runtime is what the About panel
// reads. A channel on one and not the other is a fact that is true in the API and
// missing from the screen somebody is looking at.
func TestTheChannelReachesBothReports(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.do(http.MethodGet, "/api/version", nil, nil)
	defer resp.Body.Close() //nolint:errcheck // a test response body
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("version: status = %d", resp.StatusCode)
	}
	var version map[string]string
	decode(t, resp, &version)
	if got := version["channel"]; got != model.BuildChannel {
		t.Errorf("/api/version reports channel %q, want %q", got, model.BuildChannel)
	}

	resp = h.do(http.MethodGet, "/api/settings", nil, nil)
	defer resp.Body.Close() //nolint:errcheck // a test response body
	var settings struct {
		Runtime map[string]any `json:"runtime"`
	}
	decode(t, resp, &settings)
	got, _ := settings.Runtime["channel"].(string)
	if got != model.BuildChannel {
		t.Errorf("the settings runtime reports channel %q, want %q — the About panel "+
			"reads it from there, and a channel that is in the API and not on the page "+
			"is the exact gap this was added to close.", got, model.BuildChannel)
	}
}

// TestEveryChannelHasASentence guards the terminal's half.
//
// `probeglass version` prints the channel and then says what it means, because `dev` on
// its own is a word a reader has to already know. A value added here and not to that
// switch would print "built from a working tree", which is a lie about the other two.
func TestEveryChannelHasASentence(t *testing.T) {
	was := model.BuildChannel
	t.Cleanup(func() { model.BuildChannel = was })

	seen := map[string]bool{}
	for _, channel := range []string{"release", "dev", "local"} {
		model.BuildChannel = channel
		note := model.BuildChannelNote()
		if note == "" {
			t.Errorf("the channel %q has no sentence", channel)
		}
		if seen[note] {
			t.Errorf("the channel %q says the same thing as another one: %q", channel, note)
		}
		seen[note] = true
	}

	// An unrecognised value still says something true, which is the whole reason the
	// default is `local` rather than a blank.
	model.BuildChannel = "something-else"
	if model.BuildChannelNote() == "" {
		t.Errorf("an unknown channel says nothing at all")
	}
}
