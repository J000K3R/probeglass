package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/j000k3r/weatherglass/internal/model"
)

func ptr(f float64) *float64 { return &f }

func mustSite(t *testing.T, st *Store, site model.Site) model.Site {
	t.Helper()
	if err := st.CreateSite(context.Background(), &site); err != nil {
		t.Fatalf("create site %q: %v", site.Name, err)
	}
	return site
}

// TestSiteCoordinatesRoundTrip pins the line between "not placed" and "placed at
// zero". It is the whole reason the columns are nullable: the map has to be able
// to say "this site has no coordinates" without also claiming that every site
// missing them sits in the Gulf of Guinea.
func TestSiteCoordinatesRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	mustSite(t, st, model.Site{Name: "keller", Kind: model.SiteLocal})
	mustSite(t, st, model.Site{Name: "null island", Kind: model.SiteExternal, Lat: ptr(0), Lon: ptr(0)})
	mustSite(t, st, model.Site{Name: "ffm", Kind: model.SiteExternal, Lat: ptr(50.11), Lon: ptr(8.68)})

	sites, err := st.ListSites(ctx)
	if err != nil {
		t.Fatalf("list sites: %v", err)
	}
	byName := map[string]model.Site{}
	for _, s := range sites {
		byName[s.Name] = s
	}

	if s := byName["keller"]; s.Placed() {
		t.Errorf("a site stored without coordinates came back placed at %v/%v", s.Lat, s.Lon)
	}
	s := byName["null island"]
	if !s.Placed() || *s.Lat != 0 || *s.Lon != 0 {
		t.Errorf("0/0 came back as %v/%v, want a placed site at exactly zero", s.Lat, s.Lon)
	}
	if s := byName["ffm"]; !s.Placed() || *s.Lat != 50.11 {
		t.Errorf("ffm came back as %v/%v", s.Lat, s.Lon)
	}
}

// TestSensorRefusesAnUnknownSite is the foreign key doing its job.
//
// Without it, a sensor could name a site that does not exist and the tree would
// grow a branch that leads nowhere — and the mistake would only be visible much
// later, as a sensor that never appears under any heading.
func TestSensorRefusesAnUnknownSite(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	c := model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://example.com", Site: "nirgendwo"}
	err := st.CreateSensor(ctx, &c)
	if err == nil {
		t.Fatal("a sensor pointing at a site that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "nirgendwo") {
		t.Errorf("the refusal does not name the site: %v", err)
	}
}

// TestRenamingASiteCarriesItsSensors is the ON UPDATE CASCADE.
//
// It is the payoff for referencing the name instead of a row id: a rename is one
// statement, and a sensor cannot be left behind pointing at a name that no longer
// exists — which is exactly what a hand-written update of every sensor would
// eventually do.
func TestRenamingASiteCarriesItsSensors(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	mustSite(t, st, model.Site{Name: "hoster", Kind: model.SiteExternal})
	mustSensor(t, st, model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://example.com", Site: "hoster"})

	if err := st.UpdateSite(ctx, "hoster", &model.Site{Name: "hoster-ffm", Kind: model.SiteExternal}); err != nil {
		t.Fatalf("rename the site: %v", err)
	}

	sensors, err := st.ListSensors(ctx)
	if err != nil {
		t.Fatalf("list sensors: %v", err)
	}
	if len(sensors) != 1 || sensors[0].Site != "hoster-ffm" {
		t.Fatalf("after the rename the sensor says site %q, want %q",
			sensors[0].Site, "hoster-ffm")
	}
}

// TestDeletingASiteInUseIsRefused is the ON DELETE RESTRICT, and the unassign
// that gets past it.
func TestDeletingASiteInUseIsRefused(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	mustSite(t, st, model.Site{Name: "hoster", Kind: model.SiteExternal})
	mustSensor(t, st, model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://example.com", Site: "hoster"})

	err := st.DeleteSite(ctx, "hoster", false)
	var inUse *SiteInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("deleting a site with a sensor on it gave %v, want a SiteInUseError", err)
	}
	if inUse.Count != 1 {
		t.Errorf("the refusal counted %d sensors, want 1", inUse.Count)
	}

	if err := st.DeleteSite(ctx, "hoster", true); err != nil {
		t.Fatalf("delete with unassign: %v", err)
	}
	sensors, err := st.ListSensors(ctx)
	if err != nil {
		t.Fatalf("list sensors: %v", err)
	}
	if len(sensors) != 1 {
		t.Fatalf("unassigning the site lost the sensor: %d left", len(sensors))
	}
	if sensors[0].Site != "" {
		t.Errorf("the sensor still says site %q after the site was removed", sensors[0].Site)
	}
	if _, err := st.GetSite(ctx, "hoster"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the site is still there: %v", err)
	}
}

// TestSensorWithoutASiteStoresNULL is the one that catches the empty string.
//
// A site column holding "" instead of NULL would be a foreign key pointing at a
// site named "" that no row can ever satisfy, so every sensor saved without a
// site would be refused — and the failure would look like the foreign key being
// broken rather than like the empty string being wrong.
func TestSensorWithoutASiteStoresNULL(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	c := mustSensor(t, st, model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://example.com"})
	if c.Site != "" {
		t.Fatalf("a sensor saved without a site came back with site %q", c.Site)
	}

	var isNull int
	if err := st.db.QueryRowContext(ctx,
		`SELECT site IS NULL FROM sensors WHERE id = ?`, c.ID).Scan(&isNull); err != nil {
		t.Fatalf("inspect the column: %v", err)
	}
	if isNull != 1 {
		t.Error("a sensor without a site stored something other than NULL, which the " +
			"foreign key would read as a reference to a site named \"\"")
	}

	// And it can be given one, and have it taken away again.
	if err := st.CreateSite(ctx, &model.Site{Name: "keller", Kind: model.SiteLocal}); err != nil {
		t.Fatalf("create site: %v", err)
	}
	c.Site = "keller"
	if err := st.UpdateSensor(ctx, &c); err != nil {
		t.Fatalf("assign the site: %v", err)
	}
	c.Site = ""
	if err := st.UpdateSensor(ctx, &c); err != nil {
		t.Fatalf("clear the site again: %v", err)
	}
}

// TestSiteNameIsRefusedWhenItCannotBeAURLPath keeps the name usable as the thing
// three other places assume it is: an identity, a value, and a path segment.
func TestSiteNameIsRefusedWhenItCannotBeAURLPath(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for _, name := range []string{"", "   ", "a/b", `a\b`, "a\nb"} {
		if err := st.CreateSite(ctx, &model.Site{Name: name, Kind: model.SiteLocal}); err == nil {
			t.Errorf("the site name %q was accepted", name)
		}
	}
}

// TestManagedSiteRefusesRenameAndDelete is the same rule sensors already live
// under: a row the configuration file owns cannot be changed out from under it,
// because the next apply would put it back and the change would look like it
// never happened.
func TestManagedSiteRefusesRenameAndDelete(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.UpsertManagedSite(ctx, &model.Site{
		Name: "keller", Kind: model.SiteLocal, Lat: ptr(50.0), Lon: ptr(8.0),
	}); err != nil {
		t.Fatalf("upsert managed site: %v", err)
	}

	site, err := st.GetSite(ctx, "keller")
	if err != nil {
		t.Fatalf("get site: %v", err)
	}
	if !site.Managed || !site.Placed() {
		t.Fatalf("the managed site came back as managed=%v placed=%v", site.Managed, site.Placed())
	}

	if err := st.UpdateSite(ctx, "keller", &model.Site{Name: "dachboden", Kind: model.SiteLocal}); err == nil {
		t.Error("a site declared in the configuration file was renamed from the API")
	}
	if err := st.DeleteSite(ctx, "keller", true); err == nil {
		t.Error("a site declared in the configuration file was deleted from the API")
	}

	// A second upsert is an update, not a duplicate.
	if err := st.UpsertManagedSite(ctx, &model.Site{
		Name: "keller", Kind: model.SiteLocal, Lat: ptr(51.0), Lon: ptr(9.0),
	}); err != nil {
		t.Fatalf("upsert the managed site twice: %v", err)
	}
	site, err = st.GetSite(ctx, "keller")
	if err != nil {
		t.Fatalf("get site: %v", err)
	}
	if *site.Lat != 51.0 {
		t.Errorf("the second upsert left latitude at %v, want 51", *site.Lat)
	}
}

// TestManagedSiteTakesOverARowThatAlreadyExists is the other half of the rule
// above, and the half that was missing.
//
// The test before this one upserts a site that does not exist yet, which goes
// down the create path — and the create path writes the managed column, so the
// guard works and everything looks right. The ordinary case is the other order:
// the site is made in the dashboard, or merely named by a sensor and created on
// the way past, and only later does somebody write it into the configuration
// file to give it coordinates. That second apply updates a row instead of
// inserting one, and the update used to leave `managed` at zero — so the file
// owned a site in name only, and the next person in the dashboard could rename it
// out from under the file, or delete it, and the apply after that would silently
// recreate it and put the sensors back on a site that had been "deleted".
func TestManagedSiteTakesOverARowThatAlreadyExists(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Ordinary ways to end up with the row first: the dashboard creates it, and a
	// sensor naming it lets EnsureSite make it.
	mustSite(t, st, model.Site{Name: "keller", Kind: model.SiteLocal})
	if err := st.EnsureSite(ctx, "garten"); err != nil {
		t.Fatalf("ensure site: %v", err)
	}

	for _, name := range []string{"keller", "garten"} {
		if err := st.UpsertManagedSite(ctx, &model.Site{
			Name: name, Kind: model.SiteLocal, Lat: ptr(50.0), Lon: ptr(8.0),
		}); err != nil {
			t.Fatalf("upsert %q over an existing row: %v", name, err)
		}
		site, err := st.GetSite(ctx, name)
		if err != nil {
			t.Fatalf("get %q: %v", name, err)
		}
		if !site.Managed {
			t.Errorf("after the file took over %q it is still managed=false", name)
		}
		if !site.Placed() {
			t.Errorf("%q came back without the coordinates the file declared", name)
		}
		if err := st.UpdateSite(ctx, name, &model.Site{Name: name + "-neu", Kind: model.SiteLocal}); err == nil {
			t.Errorf("%q was renamed away from the configuration file", name)
		}
		if err := st.DeleteSite(ctx, name, true); err == nil {
			t.Errorf("%q was deleted out from under the configuration file", name)
		}
	}

	// And the guard the other way round: an ordinary save may not touch the row at
	// all, its coordinates least of all. The dashboard draws a file-owned site with
	// its edit button disabled and the documentation says the file owns the name
	// and the coordinates, so a change arriving by any other route has to be
	// refused rather than accepted and then quietly put back by the next apply.
	if err := st.UpdateSite(ctx, "keller", &model.Site{
		Name: "keller", Kind: model.SiteLocal, Lat: ptr(49.0), Lon: ptr(7.0),
	}); err == nil {
		t.Error("an ordinary save changed a site the configuration file owns")
	}
	site, err := st.GetSite(ctx, "keller")
	if err != nil {
		t.Fatalf("get site: %v", err)
	}
	if !site.Managed {
		t.Error("the refusal left a managed site free")
	}
	if *site.Lat != 50.0 {
		t.Errorf("the refused save changed the coordinates anyway: lat is %v, want 50", *site.Lat)
	}
}

// TestEnsureSiteNeverOverwritesWhatIsThere is the configuration file's side of
// the bargain: it may guarantee a site exists, it may not undo someone's
// coordinates for it.
func TestEnsureSiteNeverOverwritesWhatIsThere(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	mustSite(t, st, model.Site{Name: "keller", Kind: model.SiteLocal, Lat: ptr(50.0), Lon: ptr(8.0)})

	if err := st.EnsureSite(ctx, "keller"); err != nil {
		t.Fatalf("ensure an existing site: %v", err)
	}
	if err := st.EnsureSite(ctx, "garten"); err != nil {
		t.Fatalf("ensure a new site: %v", err)
	}

	sites, err := st.ListSites(ctx)
	if err != nil {
		t.Fatalf("list sites: %v", err)
	}
	if len(sites) != 2 {
		t.Fatalf("want two sites, got %d", len(sites))
	}
	for _, s := range sites {
		if s.Name == "keller" && !s.Placed() {
			t.Error("ensuring an existing site wiped the coordinates somebody had given it")
		}
	}
}

// TestSensorCountBySite counts what the tree headings will show.
func TestSensorCountBySite(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	mustSite(t, st, model.Site{Name: "keller", Kind: model.SiteLocal})
	mustSensor(t, st, model.Sensor{Name: "a", Type: model.SensorHTTP, Target: "https://a.example", Site: "keller"})
	mustSensor(t, st, model.Sensor{Name: "b", Type: model.SensorHTTP, Target: "https://b.example", Site: "keller"})
	mustSensor(t, st, model.Sensor{Name: "c", Type: model.SensorHTTP, Target: "https://c.example"})

	counts, err := st.SensorCountBySite(ctx)
	if err != nil {
		t.Fatalf("count by site: %v", err)
	}
	if counts["keller"] != 2 {
		t.Errorf("keller holds %d sensors, want 2", counts["keller"])
	}
	if _, ok := counts[""]; ok {
		t.Error("sensors without a site were counted under the empty name")
	}
}
