package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// The box in the top bar, from the store's side.
//
// Four properties matter here and none of them can be seen from a route test that
// only checks a status code.
//
// It has to find **everything**: a word that is the name of a sensor, a place, a
// channel, an account, an outage and a probe message has to come back six times.
//
// It has to read what somebody typed as **text, not as a pattern**. A homelab's own
// vocabulary is full of `%` and `_` — "disk 92% full", "nas_backup" — and a search
// that reads them as wildcards answers a different question than the one that was
// asked, silently, and answers it with a full table scan.
//
// It has to keep **secrets** out of the answer. Every credential column in this
// schema is a plausible thing to type into a search field, and an answer that
// returns one has handed it to whoever is looking at the screen.
//
// And it has to stay **bounded**: one keystroke may not walk a table that grows
// with every probe, forever.

// zonk is one nonsense word that is in one of everything.
//
// It is made up rather than real so that a hit can only come from this fixture: a
// query like "router" would match the migration's own vocabulary in some column
// nobody thought about, and turn a passing test into a coincidental one.
const zonk = "zonk"

type searchFixture struct {
	store   *Store
	sensor  model.Sensor
	channel model.Channel
	user    model.User
}

func newSearchFixture(t *testing.T) *searchFixture {
	t.Helper()
	ctx := context.Background()
	st := newTestStore(t)

	lat, lon := 48.1372, 11.5756
	if err := st.CreateSite(ctx, &model.Site{
		Name: "Zonk Rack", Kind: model.SiteLocal, Lat: &lat, Lon: &lon,
	}); err != nil {
		t.Fatalf("create site: %v", err)
	}

	f := &searchFixture{store: st}
	f.sensor = mustSensor(t, st, model.Sensor{
		Name: "Zonk Router", Type: model.SensorPing, Target: "10.0.0.1",
		Site: "Zonk Rack", Group: "Network", Tags: []string{"lan"}, Enabled: true,
	})

	f.channel = model.Channel{
		Name: "Zonk alerts", Type: "telegram", Enabled: true,
		Config: map[string]string{"bot_token": "12345:ZONKBOTTOKEN", "chat_id": "-100"},
	}
	if err := st.CreateChannel(ctx, &f.channel); err != nil {
		t.Fatalf("create channel: %v", err)
	}

	if err := st.InsertResult(ctx, &model.Result{
		SensorID: f.sensor.ID, TS: time.Now().UTC().Add(-time.Minute),
		Success: false, Message: "zonk timed out after 5s",
	}); err != nil {
		t.Fatalf("insert result: %v", err)
	}

	if _, err := st.OpenIncident(ctx, f.sensor.ID,
		time.Now().UTC().Add(-time.Hour), "zonk refused"); err != nil {
		t.Fatalf("open incident: %v", err)
	}

	f.user = model.User{
		Username: "zonkadmin", Role: model.RoleAdmin,
		PasswordHash: "$2a$10$ZONKPASSWORDHASH", TOTPSecret: "ZONKTOTPSECRET",
	}
	if err := st.CreateUser(ctx, &f.user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return f
}

func (f *searchFixture) search(t *testing.T, opts SearchOptions) SearchResult {
	t.Helper()
	got, err := f.store.Search(context.Background(), opts)
	if err != nil {
		t.Fatalf("search %q: %v", opts.Query, err)
	}
	return got
}

// ---------------------------------------------------------------------------
// it finds everything
// ---------------------------------------------------------------------------

// TestSearchFindsOneOfEveryKind is the whole claim of the box in one assertion: a
// word that is in six different places comes back six times, once per place.
//
// A search that had stayed a sensor search would pass every other test in this file
// and fail this one, which is why it is written first.
func TestSearchFindsOneOfEveryKind(t *testing.T) {
	f := newSearchFixture(t)
	got := f.search(t, SearchOptions{Query: zonk, IncludeUsers: true})

	want := map[string]int{
		"sensor": 1, "site": 1, "channel": 1, "incident": 1, "result": 1, "user": 1,
	}
	if have := kinds(got.Hits); !sameCounts(have, want) {
		t.Fatalf("searching %q found %v, want one of each: %v", zonk, have, want)
	}
	if got.Truncated {
		t.Error("a six-row answer is reported as truncated")
	}

	// What each row says it is, checked per kind: the title is the line read first
	// and the one the browser's jump is built from.
	for _, want := range []struct{ kind, title string }{
		{"sensor", f.sensor.Name},
		{"site", "Zonk Rack"},
		{"channel", f.channel.Name},
		{"incident", "zonk refused"},
		{"result", "zonk timed out after 5s"},
		{"user", f.user.Username},
	} {
		hit := findHit(got.Hits, want.kind)
		if hit == nil {
			continue // already reported above
		}
		if hit.Title != want.title {
			t.Errorf("the %s hit is titled %q, want %q", want.kind, hit.Title, want.title)
		}
	}
}

// TestSearchReadsMoreThanTheName walks the parts of a row that are not its name.
//
// These are the words somebody actually remembers: the tag they filed a sensor
// under, the room it is in, and the sensor an outage happened to. A search that only
// knew the names would find each of these only by luck.
func TestSearchReadsMoreThanTheName(t *testing.T) {
	f := newSearchFixture(t)

	mustSensor(t, f.store, model.Sensor{
		Name: "Unrelated", Type: model.SensorTCP, Target: "10.0.0.2",
		Group: "Küche", Tags: []string{"kühlschrank"}, Enabled: true,
	})

	// A sensor is reachable by its tag and by its group, neither of which appears in
	// its name.
	if hit := findHit(f.search(t, SearchOptions{Query: "kühlschrank"}).Hits, "sensor"); hit == nil || hit.Title != "Unrelated" {
		t.Errorf("searching a tag did not find the sensor that carries it")
	}
	if hit := findHit(f.search(t, SearchOptions{Query: "Küche"}).Hits, "sensor"); hit == nil || hit.Title != "Unrelated" {
		t.Errorf("searching a group did not find the sensor in it")
	}

	// An outage is reachable by the sensor it happened to, not only by its cause —
	// "the time the NAS went" is how the person who was there remembers it.
	if n := kinds(f.search(t, SearchOptions{Query: "Zonk Router"}).Hits)["incident"]; n != 1 {
		t.Errorf("searching the sensor's name found %d outages, want the one that "+
			"happened to it", n)
	}

	// A place has only two things to be found by, and the kind is one of them.
	if hit := findHit(f.search(t, SearchOptions{Query: "local"}).Hits, "site"); hit == nil || hit.Sub != "local" {
		t.Errorf("searching a place kind did not find the local site")
	}
}

// TestSearchKeepsTheStatusItKnows puts the states the database actually holds onto
// the rows that have them.
//
// A sensor's *live* status is deliberately not among them: that lives in the
// scheduler's memory and not in a table, and the dashboard fills it in from the state
// it already has. What is here is the difference between "nothing is wrong with it"
// and "nobody is watching it", which is a column and not a probe.
func TestSearchKeepsTheStatusItKnows(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	mustSensor(t, st, model.Sensor{
		Name: "Zonk paused", Type: model.SensorTCP, Target: "10.0.0.3", Enabled: false,
	})
	running := mustSensor(t, st, model.Sensor{
		Name: "Zonk running", Type: model.SensorTCP, Target: "10.0.0.4", Enabled: true,
	})

	at := time.Now().UTC().Add(-time.Hour)
	if _, err := st.OpenIncident(ctx, running.ID, at, "zonk down"); err != nil {
		t.Fatalf("open: %v", err)
	}
	closed, err := st.OpenIncident(ctx, running.ID, at.Add(30*time.Minute), "zonk down again")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.CloseIncident(ctx, closed, at.Add(40*time.Minute)); err != nil {
		t.Fatalf("close: %v", err)
	}

	got, err := st.Search(ctx, SearchOptions{Query: "Zonk"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Hits) < 4 {
		t.Fatalf("searching %q found %d hit(s), want two sensors and two outages",
			"Zonk", len(got.Hits))
	}
	for _, hit := range got.Hits {
		switch {
		case hit.Kind == "sensor" && hit.Title == "Zonk paused" && hit.State != "paused":
			t.Errorf("the switched-off sensor is marked %q, want \"paused\"", hit.State)
		case hit.Kind == "sensor" && hit.Title == "Zonk running" && hit.State != "":
			t.Errorf("the running sensor is marked %q; a sensor's live status is the "+
				"dashboard's to fill in and not the search's", hit.State)
		case hit.Kind == "incident" && hit.State != "open" && hit.State != "resolved":
			t.Errorf("an outage is marked %q, want open or resolved", hit.State)
		}
	}
}

// ---------------------------------------------------------------------------
// what was typed is text
// ---------------------------------------------------------------------------

// TestSearchTreatsWildcardsAsLetters is the reason every query in search.go uses
// `instr` rather than `LIKE`.
//
// Each case below is a pattern as well as a phrase, and four of the five fail for a
// `LIKE`-based search in the same direction: a name comes back that was not asked
// for. `%` matches anything at all and `_` matches any one character, which is the
// whole of what goes wrong — it is silent, and the reader has no way to tell an
// over-eager match from a real one. (The fifth, the backslash, is here for a smaller
// reason: a name with one in it has to be findable at all, and a `LIKE` with an
// ESCAPE clause — six call sites, six chances to forget one — is where it stops
// being.)
//
// The names are chosen so that reading the query as a pattern finds a *second*
// sensor every time. A test that only checked the expected name came back would pass
// either way, which is why every case asserts the whole list.
func TestSearchTreatsWildcardsAsLetters(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	for _, name := range []string{
		"disk 92% full",
		"disk 92 percent full",
		"percent%%sign",
		"nas_backup",
		"nasXbackup",
		"a_b",
		"axb",
		`back\slash`,
	} {
		mustSensor(t, st, model.Sensor{
			Name: name, Type: model.SensorTCP, Target: "10.0.0.9", Enabled: true,
		})
	}

	for _, c := range []struct {
		query string
		want  []string
		why   string
	}{
		{"%%", []string{"percent%%sign"},
			"two percent signs are two characters in a name, not \"anything twice\""},
		{"92%", []string{"disk 92% full"},
			"\"disk 92 percent full\" is a different name and must not come back"},
		{"nas_", []string{"nas_backup"},
			"an underscore matches an underscore; \"nasXbackup\" is a different machine"},
		{"a_b", []string{"a_b"},
			"\"axb\" differs from \"a_b\" in exactly the position the underscore covers"},
		{`ck\s`, []string{`back\slash`},
			"a backslash is a character in a name, not an escape the query forgot to close"},
	} {
		got, err := st.Search(ctx, SearchOptions{Query: c.query})
		if err != nil {
			t.Fatalf("search %q: %v", c.query, err)
		}
		var names []string
		for _, hit := range got.Hits {
			if hit.Kind == "sensor" {
				names = append(names, hit.Title)
			}
		}
		if !sameStrings(names, c.want) {
			t.Errorf("searching %q found %v, want %v — %s", c.query, names, c.want, c.why)
		}
	}
}

// ---------------------------------------------------------------------------
// what it refuses to read
// ---------------------------------------------------------------------------

// TestSearchNeverLooksAtASecret is the security property of the whole feature.
//
// A search box is the most convenient place in a product to leak a credential from,
// because the leak does not look like one — it looks like the search working. Every
// value below is one this schema really stores, and each is searched for on its own.
// An answer of "found it" would be handing it to whoever is looking at the screen,
// which in a homelab is often a television in the living room.
func TestSearchNeverLooksAtASecret(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	mustSensor(t, st, model.Sensor{
		Name: "Secretive", Type: model.SensorHTTP, Target: "https://example.test",
		Enabled: true,
		Options: model.Options{
			"bearer_token":    "ZONKSENSORBEARER",
			"basic_auth_pass": "ZONKSENSORPASSWORD",
			"community":       "ZONKSNMPCOMMUNITY",
		},
	})
	if err := st.CreateChannel(ctx, &model.Channel{
		Name: "Secretive alerts", Type: "telegram", Enabled: true,
		Config: map[string]string{"bot_token": "ZONKBOTTOKEN", "chat_id": "-100"},
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}

	user := model.User{
		Username: "secretive", Role: model.RoleAdmin,
		PasswordHash: "$2a$10$ZONKPASSWORDHASH", TOTPSecret: "ZONKTOTPSECRET",
	}
	if err := st.CreateUser(ctx, &user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := st.ReplaceRecoveryCodes(ctx, user.ID, []string{"ZONKRECOVERYCODE"}); err != nil {
		t.Fatalf("recovery codes: %v", err)
	}
	// hashRecoveryCode is the function the column was written with, so this is the
	// exact string in the table rather than a guess at its shape.
	recoveryHash := hashRecoveryCode(user.ID, "ZONKRECOVERYCODE")

	for _, c := range []struct{ what, query string }{
		{"a sensor's bearer token", "ZONKSENSORBEARER"},
		{"a sensor's basic auth password", "ZONKSENSORPASSWORD"},
		{"an SNMP community string", "ZONKSNMPCOMMUNITY"},
		{"a channel's bot token", "ZONKBOTTOKEN"},
		{"the placeholder a stored secret comes back as", "__stored__"},
		{"an account's password hash", "ZONKPASSWORDHASH"},
		{"an account's TOTP secret", "ZONKTOTPSECRET"},
		{"a recovery code's hash", recoveryHash},
	} {
		got, err := st.Search(ctx, SearchOptions{Query: c.query, IncludeUsers: true})
		if err != nil {
			t.Fatalf("search %q: %v", c.query, err)
		}
		if len(got.Hits) != 0 {
			t.Errorf("searching for %s found %d hit(s): %v. The search box is a read, "+
				"and these are columns it must not read", c.what, len(got.Hits), got.Hits)
		}
	}

	// And the names beside those columns are still reachable, so that the checks
	// above cannot be passing because the search never ran.
	for _, want := range []string{"Secretive", "Secretive alerts", "secretive"} {
		got, err := st.Search(ctx, SearchOptions{Query: want, IncludeUsers: true})
		if err != nil {
			t.Fatalf("search %q: %v", want, err)
		}
		if len(got.Hits) == 0 {
			t.Errorf("searching the name %q found nothing, which makes the secret "+
				"checks above prove nothing", want)
		}
	}
}

// TestSearchLeavesAccountsOutUnlessAsked walks the one kind that is gated.
//
// The flag is not a convenience. A viewer may not list accounts over the API, and a
// search that answered with them anyway would be a second way round a rule the first
// way enforces. The decision is left to the caller, which is where the role is known.
func TestSearchLeavesAccountsOutUnlessAsked(t *testing.T) {
	f := newSearchFixture(t)

	plain := f.search(t, SearchOptions{Query: zonk})
	if n := kinds(plain.Hits)["user"]; n != 0 {
		t.Errorf("a search that did not ask for accounts returned %d of them", n)
	}
	if n := kinds(plain.Hits)["sensor"]; n != 1 {
		t.Errorf("a search that did not ask for accounts returned %d sensors; the other "+
			"kinds have to come back either way", n)
	}

	with := f.search(t, SearchOptions{Query: zonk, IncludeUsers: true})
	if n := kinds(with.Hits)["user"]; n != 1 {
		t.Errorf("a search that asked for accounts returned %d of them, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// and it stays bounded
// ---------------------------------------------------------------------------

// TestSearchIgnoresWhatIsTooShortToBeOne pins the floor.
//
// One character matches most of an installation, and answering it is not a search —
// it is a redraw, once per letter of whatever somebody is typing.
func TestSearchIgnoresWhatIsTooShortToBeOne(t *testing.T) {
	f := newSearchFixture(t)
	ctx := context.Background()

	for _, query := range []string{"", "   ", "\t", "z", " z ", "%"} {
		got, err := f.store.Search(ctx, SearchOptions{Query: query, IncludeUsers: true})
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if len(got.Hits) != 0 {
			t.Errorf("searching %q returned %d hit(s); it is below the minimum of %d "+
				"characters and nothing should have been read at all",
				query, len(got.Hits), SearchMinRunes)
		}
		if got.Hits == nil {
			t.Errorf("searching %q answered with a null list rather than an empty one; "+
				"the browser reads `.hits` either way, but only one of them is JSON", query)
		}
	}

	// Two characters is the shortest that runs, and it does run.
	if got, err := f.store.Search(ctx, SearchOptions{Query: "zo", IncludeUsers: true}); err != nil {
		t.Fatalf("search: %v", err)
	} else if len(got.Hits) == 0 {
		t.Errorf("a two-character query found nothing, and the fixture is full of %q", zonk)
	}
}

// TestSearchCutsALongQueryRatherThanRefusingIt pins the ceiling from the store's
// side: a query over SearchMaxRunes is shortened and answered, never refused.
//
// The sensor's name is longer than the maximum on purpose, and the query is that name
// plus a pasted tail. Cutting leaves the first 64 characters, which are in the name;
// refusing would answer nothing. So this fails for exactly one reason if the cut
// becomes a rejection.
//
// (The route above this turns an over-long query into a 400 instead, because there
// somebody can still be told what is wrong. Here, where the query is already past the
// point of being shown back to anybody, cutting is the honest answer.)
func TestSearchCutsALongQueryRatherThanRefusingIt(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	long := "Zonk" + strings.Repeat("y", SearchMaxRunes)
	mustSensor(t, st, model.Sensor{
		Name: long, Type: model.SensorTCP, Target: "10.0.0.6", Enabled: true,
	})

	tail := " and then some more"
	got, err := st.Search(ctx, SearchOptions{Query: long + tail})
	if err != nil {
		t.Fatalf("search: %v. An over-long query is cut, not an error", err)
	}
	if findHit(got.Hits, "sensor") == nil {
		t.Errorf("a query of %d characters found nothing; the first %d of it are the "+
			"name of a sensor, so the query was dropped rather than shortened",
			len([]rune(long+tail)), SearchMaxRunes)
	}
}

// TestSearchSaysWhenItWasCutShort walks the per-kind limit from both sides.
//
// The flag is the difference between "there is nothing else" and "there is more and
// I am not showing it", and a list that cannot tell those apart teaches people to
// stop trusting it. The extra row fetched to notice is the mechanism; this checks
// that the notice is used.
func TestSearchSaysWhenItWasCutShort(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	// Five match, which is more than the limit asked for below and fewer than the
	// default the dashboard uses.
	for _, name := range []string{"Zonk one", "Zonk two", "Zonk three", "Zonk four", "Zonk five"} {
		mustSensor(t, st, model.Sensor{
			Name: name, Type: model.SensorTCP, Target: "10.0.0.9", Enabled: true,
		})
	}

	all, err := st.Search(ctx, SearchOptions{Query: zonk})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(all.Hits) != 5 || all.Truncated {
		t.Fatalf("the unbounded search returned %d hit(s) (truncated=%v), want all 5",
			len(all.Hits), all.Truncated)
	}

	cut, err := st.Search(ctx, SearchOptions{Query: zonk, PerKind: 2})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(cut.Hits) != 2 {
		t.Errorf("a per-kind limit of 2 returned %d hit(s)", len(cut.Hits))
	}
	if !cut.Truncated {
		t.Error("five matches under a limit of two is not reported as truncated; the " +
			"list would then claim to be everything there is")
	}

	// Above the maximum the caller is clamped rather than obeyed. The ceiling exists
	// to keep one request from filling a response, and a query string is not a reason
	// to lift it.
	huge, err := st.Search(ctx, SearchOptions{Query: zonk, PerKind: 100000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(huge.Hits) != 5 || huge.Truncated {
		t.Errorf("asking for 100000 per kind returned %d hit(s) (truncated=%v); only 5 "+
			"exist, and a clamped limit still covers them", len(huge.Hits), huge.Truncated)
	}
}

// TestSearchResultsStayInsideTheRetentionWindow walks the one table that grows
// without limit.
//
// `results` holds a row per probe per sensor and is never pruned to nothing, so the
// window is what keeps a keystroke from walking all of it. The old row below is not
// out-competed by the limit — it is the only match there is — so this fails for
// exactly one reason if the window is dropped.
func TestSearchResultsStayInsideTheRetentionWindow(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	raw, _ := st.Retention()
	if raw <= 0 {
		t.Fatalf("the store reports no raw retention window, so this test cannot mean anything")
	}
	sensor := mustSensor(t, st, model.Sensor{
		Name: "Zonk archive", Type: model.SensorTCP, Target: "10.0.0.5", Enabled: true,
	})

	for _, c := range []struct {
		what string
		at   time.Time
		want bool
	}{
		{"a message from a minute ago", time.Now().UTC().Add(-time.Minute), true},
		{"a message from just inside the window", time.Now().UTC().Add(-raw + time.Hour), true},
		{"a message from before the window", time.Now().UTC().Add(-raw - 24*time.Hour), false},
	} {
		message := "zonk " + c.what
		if err := st.InsertResult(ctx, &model.Result{
			SensorID: sensor.ID, TS: c.at, Success: false, Message: message,
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
		got, err := st.Search(ctx, SearchOptions{Query: message})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if found := findHit(got.Hits, "result") != nil; found != c.want {
			t.Errorf("%s is %v, want %v. Without the window every keystroke walks the "+
				"whole probe history of the installation", c.what, found, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// small helpers for comparing whole answers
// ---------------------------------------------------------------------------

func findHit(hits []SearchHit, kind string) *SearchHit {
	for i := range hits {
		if hits[i].Kind == kind {
			return &hits[i]
		}
	}
	return nil
}

// kinds counts an answer by kind, which is how these tests compare a whole result
// list without pinning the order the six queries came back in.
func kinds(hits []SearchHit) map[string]int {
	out := map[string]int{}
	for _, hit := range hits {
		out[hit.Kind]++
	}
	return out
}

func sameCounts(a, b map[string]int) bool {
	for k, want := range b {
		if a[k] != want {
			return false
		}
	}
	for k, have := range a {
		if have != 0 && b[k] == 0 {
			return false
		}
	}
	return true
}

// sameStrings compares two sets of names, ignoring order — which the tests above do
// not pin, because it is the database's ordering and not a fact about the search.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	return true
}
