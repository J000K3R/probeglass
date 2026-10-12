package api

import (
	"io/fs"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/web"
)

// The list of languages lives twice. Once here, in allowedUIValues, where it
// decides what may be written to a user row; once in the dashboard, in
// `available` inside i18n.js, where it is what the picker offers. Two copies of
// one list is the shape this repository has met before — model.SensorTypes,
// model.Layouts, the widget registry — and the failure is always the same and
// always silent in one direction: a language the server accepts and the browser
// cannot load is stored, ignored, and looks like a setting that does not work,
// while one the browser offers and the server refuses comes back as a failed save
// with no explanation.

// shippedLanguages reads the picker's list out of the asset the browser loads.
func shippedLanguages(t *testing.T) []string {
	t.Helper()
	src, err := fs.ReadFile(web.Assets(), "i18n.js")
	if err != nil {
		t.Fatalf("read i18n.js from the embedded dashboard: %v", err)
	}
	m := regexp.MustCompile(`(?m)^export const available = \[(.*)\];$`).FindStringSubmatch(string(src))
	if m == nil {
		t.Fatal("i18n.js no longer exports `available`; the language picker is built " +
			"from that list and this test cannot see what ships without it")
	}
	var codes []string
	for _, raw := range strings.Split(m[1], ",") {
		if code := strings.Trim(strings.TrimSpace(raw), `'"`); code != "" {
			codes = append(codes, code)
		}
	}
	return codes
}

// TestEveryShippedLanguageIsStorable is the guard on those two copies.
func TestEveryShippedLanguageIsStorable(t *testing.T) {
	shipped := shippedLanguages(t)
	storable := allowedUIValues[model.PrefLanguage]
	if len(shipped) == 0 || len(storable) == 0 {
		t.Fatalf("a language list is empty: %d shipped, %d storable", len(shipped), len(storable))
	}

	for _, code := range shipped {
		if !slices.Contains(storable, code) {
			t.Errorf("the dashboard ships the language %q and the server would refuse to "+
				"store it.\nThe picker offers it, the save comes back refused, and the "+
				"switch appears to do nothing — add it to allowedUIValues[PrefLanguage].",
				code)
		}
	}
	for _, code := range storable {
		if !slices.Contains(shipped, code) {
			t.Errorf("the server stores the language %q and the dashboard does not ship "+
				"it.\nThe choice is saved, the dashboard has no catalogue for it, and "+
				"the interface quietly stays as it was — either add the catalogue or take "+
				"the code out of allowedUIValues.", code)
		}
	}
}

// TestAShippedLanguageCanBeChosenAndReadBack walks one non-default language
// through the real endpoint.
//
// The test above is about the two lists agreeing; this one is about the road
// between them: a preference is written, stored and read back, and the value that
// arrives in the dashboard is the one that was chosen. It would pass for English
// without any of this being true, which is why it asks for the last language the
// picker offers rather than the first.
func TestAShippedLanguageCanBeChosenAndReadBack(t *testing.T) {
	h := newHarness(t)
	h.setup()
	origin := map[string]string{"Origin": h.http.URL}

	shipped := shippedLanguages(t)
	chosen := shipped[len(shipped)-1]

	resp := h.do(http.MethodPut, "/api/settings",
		map[string]any{"ui": map[string]string{"language": chosen}}, origin)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("choosing the language %q: status = %d, want 200 — the picker offers "+
			"it, so the server has to accept it", chosen, resp.StatusCode)
	}

	var settings struct {
		UI map[string]string `json:"ui"`
	}
	decode(t, h.do(http.MethodGet, "/api/settings", nil, nil), &settings)
	if got := settings.UI["language"]; got != chosen {
		t.Errorf("the language read back is %q, want %q", got, chosen)
	}

	// And a code nobody ships is still refused, so that the check above is not
	// passing merely because everything is accepted.
	resp = h.do(http.MethodPut, "/api/settings",
		map[string]any{"ui": map[string]string{"language": "kl"}}, origin)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a language that does not exist gave status %d, want 400", resp.StatusCode)
	}
}
