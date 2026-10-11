package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/j000k3r/weatherglass/internal/model"
)

// Sites are places sensors live. These tests are about the three promises the
// feature makes: a site can be created and renamed without disturbing the sensors
// on it, one that is still in use cannot vanish out from under them, and the map's
// tile source is a setting the content policy actually follows.

// readBody returns a response body as a string and closes it. Several assertions
// here are about what the error message says, which only the raw body holds.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the response body: %v", err)
	}
	return string(raw)
}

// createSite posts a site and returns it.
func createSite(t *testing.T, h *harness, in map[string]any) model.Site {
	t.Helper()
	resp := h.do(http.MethodPost, "/api/sites", in, origin(h))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create site %v: status = %d, body = %s",
			in["name"], resp.StatusCode, readBody(t, resp))
	}
	var site model.Site
	decode(t, resp, &site)
	return site
}

// placeSensor creates a sensor, optionally at a site.
func placeSensor(t *testing.T, h *harness, name, site string) model.Sensor {
	t.Helper()
	body := map[string]any{
		"name": name, "type": "http", "target": "https://example.com",
	}
	if site != "" {
		body["site"] = site
	}
	resp := h.do(http.MethodPost, "/api/sensors", body, origin(h))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create sensor %q: status = %d, body = %s",
			name, resp.StatusCode, readBody(t, resp))
	}
	var c model.Sensor
	decode(t, resp, &c)
	return c
}

// siteNamed picks one site out of the list.
func siteNamed(sites []model.Site, name string) (model.Site, bool) {
	for _, s := range sites {
		if s.Name == name {
			return s, true
		}
	}
	return model.Site{}, false
}

// listSites reads the site list back the way the dashboard does.
func (h *harness) listSites() []model.Site {
	h.t.Helper()
	resp := h.do(http.MethodGet, "/api/sites", nil, nil)
	var body struct {
		Sites []model.Site `json:"sites"`
	}
	decode(h.t, resp, &body)
	return body.Sites
}

// TestSiteRoundTrip covers the plain path: create, place a sensor, rename, and
// find the sensor still on it.
func TestSiteRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.setup()

	lat, lon := 50.11, 8.68
	created := createSite(t, h, map[string]any{
		"name": "keller", "kind": "local", "lat": lat, "lon": lon,
	})
	if !created.Placed() {
		t.Fatalf("the site came back without coordinates: %+v", created)
	}
	if created.Kind != model.SiteLocal {
		t.Errorf("the site came back as kind %q", created.Kind)
	}

	if c := placeSensor(t, h, "web", "keller"); c.Site != "keller" {
		t.Fatalf("the sensor was stored with site %q", c.Site)
	}

	// Renaming the site must carry the sensor with it. This is the payoff for the
	// foreign key pointing at the name: it is one UPDATE, and no sensor can be
	// left behind pointing at a name that no longer exists.
	resp := h.do(http.MethodPut, "/api/sites/keller", map[string]any{
		"name": "dachboden", "kind": "local", "lat": lat, "lon": lon,
	}, origin(h))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}

	// The rename answers with the site as it now stands, and that answer carries
	// how many sensors are on it. This is the route where the number cannot be
	// known before the write: the sensors arrive at the row through the cascade,
	// so a body built from the request alone would report a busy site as empty.
	var renamed model.Site
	decode(t, resp, &renamed)
	if renamed.SensorCount != 1 {
		t.Errorf("the rename answered with %d sensors, want 1", renamed.SensorCount)
	}

	for _, c := range h.listSensors() {
		if c.Name == "web" && c.Site != "dachboden" {
			t.Errorf("after the rename the sensor says site %q, want %q", c.Site, "dachboden")
		}
	}

	// And the count travels with the list, because the tree headings and the
	// delete warning both read it.
	site, ok := siteNamed(h.listSites(), "dachboden")
	if !ok {
		t.Fatal("the site is gone after the rename")
	}
	if site.SensorCount != 1 {
		t.Errorf("the site reports %d sensors, want 1", site.SensorCount)
	}
}

// TestTheSummaryCountsTheSensorsPerSite is a regression test with a story.
//
// The dashboard reads its site list from /api/sites only when it is opened there
// first; a browser that went through the dashboard already has the sites, and it
// got them from /api/summary. That copy used to carry no counts — the handler
// listed the rows and left `sensor_count` at zero — so the Settings panel said
// every site was empty while the tree beside it plainly grouped four sensors. The
// counts are now filled from the sensors the handler already has in hand, and
// this asserts the two endpoints agree.
func TestTheSummaryCountsTheSensorsPerSite(t *testing.T) {
	h := newHarness(t)
	h.setup()
	createSite(t, h, map[string]any{"name": "keller", "kind": "local"})
	placeSensor(t, h, "web", "keller")
	placeSensor(t, h, "dns", "keller")
	placeSensor(t, h, "stray", "")

	resp := h.do(http.MethodGet, "/api/summary", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summary: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var body struct {
		Sites []model.Site `json:"sites"`
	}
	decode(t, resp, &body)

	site, ok := siteNamed(body.Sites, "keller")
	if !ok {
		t.Fatal("the summary did not carry the site at all")
	}
	if site.SensorCount != 2 {
		t.Errorf("the summary reports %d sensors on the site, want 2", site.SensorCount)
	}
	// And the endpoint the panel falls back to says the same thing, so a person
	// who arrives by either road sees one number.
	if listed, _ := siteNamed(h.listSites(), "keller"); listed.SensorCount != site.SensorCount {
		t.Errorf("/api/sites says %d and /api/summary says %d",
			listed.SensorCount, site.SensorCount)
	}
}

// TestDeletingASiteInUseSaysHowMany is the refusal, and that it is useful.
//
// A bare 409 would leave whoever asked with nothing to do next. The count is what
// turns it into a decision: move them, or take the site away and leave them
// without one.
func TestDeletingASiteInUseSaysHowMany(t *testing.T) {
	h := newHarness(t)
	h.setup()
	createSite(t, h, map[string]any{"name": "keller", "kind": "local"})
	placeSensor(t, h, "web", "keller")

	resp := h.do(http.MethodDelete, "/api/sites/keller", nil, origin(h))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("deleting a site in use: status = %d, want 409; body = %s",
			resp.StatusCode, readBody(t, resp))
	}
	if body := readBody(t, resp); !strings.Contains(body, "sensor") {
		t.Errorf("the refusal does not say what is in the way: %q", body)
	}

	// With unassign it goes, and the sensor survives without a site.
	resp = h.do(http.MethodDelete, "/api/sites/keller?unassign=1", nil, origin(h))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete with unassign: status = %d, body = %s",
			resp.StatusCode, readBody(t, resp))
	}
	found := false
	for _, c := range h.listSensors() {
		if c.Name == "web" {
			found = true
			if c.Site != "" {
				t.Errorf("the sensor still says site %q", c.Site)
			}
		}
	}
	if !found {
		t.Error("deleting the site lost the sensor that was on it")
	}
}

// TestSensorCannotBeGivenASiteThatDoesNotExist keeps the foreign key honest
// through the API: a mistyped site name is the caller's mistake, so it is a 400
// with the name in it rather than a 500 that sends somebody looking for a bug.
func TestSensorCannotBeGivenASiteThatDoesNotExist(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.do(http.MethodPost, "/api/sensors", map[string]any{
		"name": "web", "type": "http", "target": "https://example.com", "site": "nirgendwo",
	}, origin(h))
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "nirgendwo") {
		t.Errorf("the error does not name the site: %q", body)
	}
}

// TestSiteCoordinatesAreOptionalButComplete pins the rule that keeps the map
// honest: a site either has a place or it does not, and half a coordinate pair is
// neither.
func TestSiteCoordinatesAreOptionalButComplete(t *testing.T) {
	h := newHarness(t)
	h.setup()

	// No coordinates at all is fine — that is a site that groups and shows and is
	// simply not drawn.
	createSite(t, h, map[string]any{"name": "ohne", "kind": "local"})

	// Half of a pair is refused rather than guessed at.
	resp := h.do(http.MethodPost, "/api/sites", map[string]any{
		"name": "halb", "kind": "local", "lat": 50.0,
	}, origin(h))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("half a coordinate was accepted: status = %d", resp.StatusCode)
	}

	// And 0/0 is a real place, not a missing one.
	site := createSite(t, h, map[string]any{
		"name": "null island", "kind": "external", "lat": 0, "lon": 0,
	})
	if !site.Placed() {
		t.Error("a site at exactly 0/0 came back as unplaced: that would make the " +
			"one coordinate pair indistinguishable from a default mean the wrong thing")
	}
}

// TestSummaryCarriesSitesAndHeartbeat covers the two additions to the payload the
// dashboard draws from. Every layout but the plain cards needs one of them, and
// neither is optional in the JSON — a missing field and an empty one mean
// different things to the client.
func TestSummaryCarriesSitesAndHeartbeat(t *testing.T) {
	h := newHarness(t)
	h.setup()
	createSite(t, h, map[string]any{"name": "keller", "kind": "local"})

	resp := h.do(http.MethodGet, "/api/summary", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summary: status = %d", resp.StatusCode)
	}
	var body struct {
		Sites []model.Site `json:"sites"`
		Views []struct {
			Name      string `json:"name"`
			Heartbeat []bool `json:"heartbeat"`
		} `json:"sensors"`
	}
	decode(t, resp, &body)

	if len(body.Sites) != 1 {
		t.Errorf("the summary carries %d sites, want 1", len(body.Sites))
	}
	if len(body.Views) == 0 {
		t.Fatal("the summary has no sensors")
	}
	// The heartbeat is a list per sensor, not a single flag: the wall draws one
	// mark per check, so a sensor that has never run reports an empty list rather
	// than being absent from the payload.
	for _, v := range body.Views {
		if v.Heartbeat == nil {
			t.Errorf("sensor %q has no heartbeat field at all", v.Name)
		}
	}
}

// TestTileURLSettingFollowsIntoTheContentPolicy is the one that catches the silent
// failure.
//
// A tile URL the content policy does not allow produces a map with no tiles and no
// error anywhere the user can see — a browser reports a CSP refusal only in a
// console nobody opens. So the origin has to appear in the header once an address is
// configured, and it has to be absent while none is: the whole point of shipping no
// tile address is that the browser asks nobody, and a policy still naming a tile host
// would be a promise the rest of the code no longer keeps.
func TestTileURLSettingFollowsIntoTheContentPolicy(t *testing.T) {
	h := newHarness(t)
	h.setup()

	// Nothing is configured out of the box, so nothing is permitted.
	resp := h.do(http.MethodGet, "/api/settings", nil, nil)
	csp := resp.Header.Get("Content-Security-Policy")
	readBody(t, resp)
	if !strings.Contains(csp, "img-src 'self' data:;") {
		t.Errorf("with no tile server configured the policy is %q; it should allow "+
			"images from this origin and nothing else, so that the map can draw "+
			"itself without asking anybody", csp)
	}
	if strings.Contains(csp, "openstreetmap.org") {
		t.Errorf("the policy allows a third party's tiles out of the box: %q", csp)
	}

	// Point it somewhere else.
	put := h.do(http.MethodPut, "/api/map", map[string]any{
		"tile_url":    "https://tiles.example.org/{z}/{x}/{y}.png",
		"attribution": "© Beispiel",
	}, origin(h))
	if put.StatusCode != http.StatusOK {
		t.Fatalf("set the tile URL: status = %d, body = %s", put.StatusCode, readBody(t, put))
	}

	resp = h.do(http.MethodGet, "/api/settings", nil, nil)
	csp = resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "https://tiles.example.org") {
		t.Errorf("after configuring a tile URL the policy is %q, which does not allow it", csp)
	}

	// And the settings screen can show what is set.
	var body struct {
		Map struct {
			TileURL     string `json:"tile_url"`
			Attribution string `json:"attribution"`
		} `json:"map"`
	}
	decode(t, resp, &body)
	if body.Map.TileURL != "https://tiles.example.org/{z}/{x}/{y}.png" {
		t.Errorf("the settings report the tile URL as %q", body.Map.TileURL)
	}
	if body.Map.Attribution != "© Beispiel" {
		t.Errorf("the settings report the attribution as %q", body.Map.Attribution)
	}

	// Clearing the field is a decision, not an empty form: the address goes and the
	// permission goes with it. If the policy kept the origin after this, the setting
	// would read as "nothing is fetched" while the browser was still allowed to.
	clear := h.do(http.MethodPut, "/api/map", map[string]any{
		"tile_url": "", "attribution": "© Beispiel",
	}, origin(h))
	if clear.StatusCode != http.StatusOK {
		t.Fatalf("clear the tile URL: status = %d, body = %s", clear.StatusCode, readBody(t, clear))
	}
	resp = h.do(http.MethodGet, "/api/settings", nil, nil)
	csp = resp.Header.Get("Content-Security-Policy")
	decode(t, resp, &body)
	if strings.Contains(csp, "tiles.example.org") {
		t.Errorf("the policy still allows the removed tile host: %q", csp)
	}
	if body.Map.TileURL != "" {
		t.Errorf("after clearing it the settings report the tile URL as %q", body.Map.TileURL)
	}
}

// TestTileURLIsValidatedOnlyWhenItIsSet turns an empty map into a sentence next to
// the field that is wrong — and keeps the empty field itself out of that.
//
// A template missing {z}/{x}/{y} asks for the same address for every tile there is,
// which a browser answers by fetching one image and stretching it across the map — a
// failure with no error attached to it anywhere. But none of that applies to an
// address that is not there, and a form that refuses to let somebody turn the tiles
// off is a form with a setting nobody can unset.
func TestTileURLIsValidatedOnlyWhenItIsSet(t *testing.T) {
	h := newHarness(t)
	h.setup()

	for _, bad := range []string{
		"https://tiles.example.org/tile.png",
		"https://tiles.example.org/{z}/{x}.png",
		"not a url at all",
		"ftp://tiles.example.org/{z}/{x}/{y}.png",
		"http:///{z}/{x}/{y}.png",
	} {
		resp := h.do(http.MethodPut, "/api/map", map[string]any{"tile_url": bad}, origin(h))
		readBody(t, resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("the tile URL %q was accepted: status = %d", bad, resp.StatusCode)
		}
	}

	// Whitespace is the same as empty: a field somebody tabbed through and then
	// cleared must not come back as a validation error.
	for _, empty := range []string{"", "   ", "\t"} {
		resp := h.do(http.MethodPut, "/api/map", map[string]any{"tile_url": empty}, origin(h))
		readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("clearing the tile URL with %q was refused: status = %d", empty, resp.StatusCode)
		}
	}
}

// TestSitesAreReadableButOnlyWritableByAdmins is the same line the rest of the
// product draws: everyone can see the shape of the place, only an administrator
// can change it.
func TestSitesAreReadableButOnlyWritableByAdmins(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.addUser(t, "viewer", "viewer1234", model.RoleViewer)
	viewer := h.signIn("viewer", "viewer1234")

	resp := h.as(viewer, func() *http.Response {
		return h.do(http.MethodGet, "/api/sites", nil, nil)
	})
	readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("a viewer cannot read the sites: status = %d", resp.StatusCode)
	}

	for _, tc := range []struct {
		method, path string
		body         map[string]any
	}{
		{http.MethodPost, "/api/sites", map[string]any{"name": "x", "kind": "local"}},
		{http.MethodPut, "/api/sites/x", map[string]any{"name": "x", "kind": "local"}},
		{http.MethodDelete, "/api/sites/x", nil},
		{http.MethodPut, "/api/map",
			map[string]any{"tile_url": "https://t.example.org/{z}/{x}/{y}.png"}},
	} {
		resp := h.as(viewer, func() *http.Response {
			return h.do(tc.method, tc.path, tc.body, origin(h))
		})
		readBody(t, resp)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as a viewer: status = %d, want 403", tc.method, tc.path, resp.StatusCode)
		}
	}
}

// TestASiteTheFileOwnsCannotBeEditedFromTheAPI is the route-level half of the
// store rule, and the half a person actually meets.
//
// The dashboard already draws a file-owned site with its edit and delete buttons
// disabled, so this is not a button anyone can press in a browser — it is the
// answer somebody gets from a script, a stale tab, or curl. What it must not be is
// a 200 followed by the change quietly disappearing on the next apply: that is the
// failure that reads as a bug in Weatherglass rather than as a rule.
func TestASiteTheFileOwnsCannotBeEditedFromTheAPI(t *testing.T) {
	h := newHarness(t)
	h.setup()
	createSite(t, h, map[string]any{"name": "keller", "kind": "local", "lat": 50.0, "lon": 8.0})

	// The configuration file takes the row over, the way an apply would.
	lat, lon := 50.0, 8.0
	if err := h.store.UpsertManagedSite(context.Background(), &model.Site{
		Name: "keller", Kind: model.SiteLocal, Lat: &lat, Lon: &lon,
	}); err != nil {
		t.Fatalf("upsert managed site: %v", err)
	}

	for _, tc := range []struct {
		what string
		body map[string]any
	}{
		{"new coordinates", map[string]any{"name": "keller", "kind": "local", "lat": 49.0, "lon": 7.0}},
		{"a new kind", map[string]any{"name": "keller", "kind": "external", "lat": 50.0, "lon": 8.0}},
		{"a rename", map[string]any{"name": "dachboden", "kind": "local", "lat": 50.0, "lon": 8.0}},
	} {
		resp := h.do(http.MethodPut, "/api/sites/keller", tc.body, origin(h))
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("changing %s of a file-owned site: status = %d, want 409; body = %s",
				tc.what, resp.StatusCode, body)
		}
	}

	// Nothing went through, and the row still says what the file says.
	sites, _ := siteNamed(h.listSites(), "keller")
	if sites.Kind != model.SiteLocal {
		t.Errorf("the refused edit changed the kind to %q", sites.Kind)
	}
	if !sites.Placed() || *sites.Lat != 50.0 {
		t.Errorf("the refused edit moved the site to %v/%v", sites.Lat, sites.Lon)
	}

	// And the delete is refused for the same reason.
	resp := h.do(http.MethodDelete, "/api/sites/keller?unassign=1", nil, origin(h))
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("deleting a file-owned site: status = %d, want 409; body = %s",
			resp.StatusCode, body)
	}
}
