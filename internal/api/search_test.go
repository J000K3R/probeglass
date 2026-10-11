package api

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
)

// GET /api/search, from the route's side: what a caller is allowed to ask and what
// they get told when they ask wrongly.
//
// The store test next door covers what a search finds and what it refuses to read.
// The three things left over are the route's own: the account gate is decided here
// rather than in the browser, an over-long query is a 400 rather than a silent
// nothing, and a query too short to run is not an error at all.

// searchFixture puts one of everything behind the API, all sharing one nonsense word.
//
// The word is made up so that a hit can only come from this fixture — a real word
// would match the migration's own vocabulary somewhere and turn a passing test into
// a coincidental one.
const zonkTerm = "zonk"

type searchRouteFixture struct {
	h       *harness
	sensor  model.Sensor
	channel model.Channel
	account model.User
}

func newSearchRouteFixture(t *testing.T) *searchRouteFixture {
	t.Helper()
	h := newHarness(t)
	h.setup() // signs in as testUser, an administrator
	ctx := context.Background()

	f := &searchRouteFixture{h: h}

	f.sensor = model.Sensor{
		Name: "Zonk Router", Type: model.SensorPing, Target: "10.0.0.1",
		Interval: 60, Timeout: 5, Retries: 1, Enabled: true,
		Options: model.Options{"bearer_token": "ZONKSENSORBEARER"},
	}
	store.NormaliseSensor(&f.sensor)
	if err := h.store.CreateSensor(ctx, &f.sensor); err != nil {
		t.Fatalf("create sensor: %v", err)
	}

	f.channel = model.Channel{
		Name: "Zonk alerts", Type: "telegram", Enabled: true,
		Config: map[string]string{"bot_token": "ZONKBOTTOKEN", "chat_id": "-100"},
	}
	if err := h.store.CreateChannel(ctx, &f.channel); err != nil {
		t.Fatalf("create channel: %v", err)
	}

	f.account = h.addUser(t, "zonkviewer", "zonkviewer1234", model.RoleViewer)
	return f
}

// search asks for a query and gives back the status and the decoded body.
func (f *searchRouteFixture) search(t *testing.T, query string) (int, store.SearchResult) {
	t.Helper()
	resp := f.h.do(http.MethodGet, "/api/search?q="+url.QueryEscape(query), nil, nil)
	var out store.SearchResult
	decode(t, resp, &out)
	return resp.StatusCode, out
}

// searchRaw is search without decoding, for the test that has to look at the bytes
// of the answer rather than at the fields they parse into.
func (f *searchRouteFixture) searchRaw(t *testing.T, query string) (int, string) {
	t.Helper()
	resp := f.h.do(http.MethodGet, "/api/search?q="+url.QueryEscape(query), nil, nil)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(raw)
}

func kindsOf(hits []store.SearchHit) map[string]int {
	out := map[string]int{}
	for _, hit := range hits {
		out[hit.Kind]++
	}
	return out
}

// TestSearchIsAVeriewerRouteWithOneException walks the account gate at the place it
// is decided.
//
// A viewer may read everything else the search looks at — the sensors, the places,
// the channels, the outages — so the route is deliberately open to them. Accounts are
// the exception, and the check is here rather than in the browser because a rule the
// browser applies is a rule somebody can edit the query string around: asking for
// `includeUsers=1` or a different `Accept` must change nothing at all.
func TestSearchOnlyWithholdsAccountsFromAViewer(t *testing.T) {
	f := newSearchRouteFixture(t)
	admin := f.h.cookie
	viewer := f.h.signIn("zonkviewer", "zonkviewer1234")

	// The administrator sees the account, and the sensor beside it, so the viewer's
	// answer below is not empty for some unrelated reason.
	status, got := f.search(t, zonkTerm)
	if status != http.StatusOK {
		t.Fatalf("an administrator's search returned %d", status)
	}
	if kindsOf(got.Hits)["user"] != 1 {
		t.Fatalf("an administrator's search found %d account(s), want the one that "+
			"matches", kindsOf(got.Hits)["user"])
	}

	f.h.as(viewer, func() *http.Response {
		// The plain request, and the same one with the query string widened by hand.
		for _, query := range []string{
			"/api/search?q=" + zonkTerm,
			"/api/search?q=" + zonkTerm + "&includeUsers=1&users=1&admin=1",
		} {
			resp := f.h.do(http.MethodGet, query, nil, nil)
			var asViewer store.SearchResult
			decode(t, resp, &asViewer)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("a viewer's search returned %d", resp.StatusCode)
			}
			if n := kindsOf(asViewer.Hits)["user"]; n != 0 {
				t.Errorf("%s returned %d account(s) to a viewer; the role is decided "+
					"on the server, so a query string cannot widen it", query, n)
			}
			// Everything else is still there. A viewer whose search answered nothing
			// at all would pass the check above and be a broken product.
			if kindsOf(asViewer.Hits)["sensor"] != 1 {
				t.Errorf("%s found %d sensor(s) for a viewer, want 1", query,
					kindsOf(asViewer.Hits)["sensor"])
			}
			if kindsOf(asViewer.Hits)["channel"] != 1 {
				t.Errorf("%s found %d channel(s) for a viewer, want 1", query,
					kindsOf(asViewer.Hits)["channel"])
			}
		}
		return nil
	})

	// And the administrator's session is back the way it was.
	if f.h.cookie != admin {
		t.Error("the harness did not put the administrator's session back")
	}
}

// TestSearchNeverSendsASecretInAnyKind is the belt to the store test's braces.
//
// The store test proves the secret columns are not searched, one column at a time. It
// can only prove that for the columns somebody thought of. This one asks the route
// the question a leak would actually be: with a query that matches a real row of
// every kind, is the credential anywhere in the bytes of the answer? A kind added
// later that carries a config along with it fails here without anybody having to
// remember to extend a list.
func TestSearchNeverSendsASecretInAnyKind(t *testing.T) {
	f := newSearchRouteFixture(t)

	status, body := f.searchRaw(t, zonkTerm)
	if status != http.StatusOK {
		t.Fatalf("search returned %d", status)
	}
	if !strings.Contains(body, f.sensor.Name) {
		t.Fatalf("the answer does not mention %q, so this test would pass on nothing: %s",
			f.sensor.Name, body)
	}
	for _, secret := range []string{"ZONKSENSORBEARER", "ZONKBOTTOKEN", "__stored__"} {
		if strings.Contains(body, secret) {
			t.Errorf("the search answer contains %s; a search box that hands back a "+
				"credential is worse than one that finds nothing", secret)
		}
	}
}

// TestSearchRefusesAnOverlongQueryAtTheRoute is the one place the two layers disagree,
// on purpose.
//
// The store cuts an over-long query and answers, because at that depth there is nobody
// left to tell. Here somebody typed it, and they can be told: a 400 with a sentence
// beats a truncated search that returns nothing and looks like a search that found
// nothing. The boundary is worth pinning from both sides — one character under it is
// an ordinary request.
func TestSearchRefusesAnOverlongQueryAtTheRoute(t *testing.T) {
	f := newSearchRouteFixture(t)

	status, body := f.searchRaw(t, strings.Repeat("z", store.SearchMaxRunes+1))
	if status != http.StatusBadRequest {
		t.Errorf("a query of %d characters returned %d, want 400", store.SearchMaxRunes+1, status)
	}
	if !strings.Contains(body, "error") {
		t.Errorf("the refusal says nothing: %s", body)
	}

	// Exactly at the limit is allowed, and answered — the store has nothing to match
	// here, so an empty list is the whole of the expectation.
	status, _ = f.searchRaw(t, strings.Repeat("z", store.SearchMaxRunes))
	if status != http.StatusOK {
		t.Errorf("a query of exactly %d characters returned %d, want 200",
			store.SearchMaxRunes, status)
	}
}

// TestSearchAnswersSomethingTooShortWithNothing pins the other end.
//
// Somebody typing into a search field one letter at a time is doing nothing wrong, and
// the letters they type before the second one arrive on the wire. Answering those with
// a red error toast would teach them to stop typing, so the answer is an empty list, and
// it is an empty list rather than a null one because the browser reads `.hits` on it.
func TestSearchAnswersSomethingTooShortWithNothing(t *testing.T) {
	f := newSearchRouteFixture(t)

	// The bare route, an empty query, one letter, and one letter behind the
	// whitespace the handler trims before it counts.
	for _, path := range []string{
		"/api/search",
		"/api/search?q=",
		"/api/search?q=z",
		"/api/search?q=" + url.QueryEscape("  z"),
	} {
		resp := f.h.do(http.MethodGet, path, nil, nil)
		var got store.SearchResult
		decode(t, resp, &got)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s returned %d; a query too short to run is not an error",
				path, resp.StatusCode)
			continue
		}
		if got.Hits == nil {
			t.Errorf("%s answered with a null list; the browser reads `.hits` on it", path)
		}
		if len(got.Hits) != 0 {
			t.Errorf("%s returned %d hit(s); the fixture matches a one-letter query on "+
				"nothing, so anything here means the floor is not applied",
				path, len(got.Hits))
		}
	}
}

// TestSearchRefusesToBeAWrite checks the method, which is the cheapest thing in the
// file and the one that would be embarrassing to get wrong: the box in the top bar
// reads, and a GET that a script could turn into a POST is a GET that mutates.
func TestSearchRefusesToBeAWrite(t *testing.T) {
	f := newSearchRouteFixture(t)

	resp := f.h.do(http.MethodPost, "/api/search?q="+zonkTerm, map[string]string{"q": zonkTerm}, origin(f.h))
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /api/search returned %d; the search reads and does not write",
			resp.StatusCode)
	}
}
