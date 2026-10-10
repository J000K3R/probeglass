package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/j000k3r/weatherglass/internal/model"
)

// exampleSensorSet is a small stand-in for the dataset the Settings button
// writes. Only the names matter here: they are what the uniqueness check is
// about, and what the collision case is built from.
func exampleSensorSet(names ...string) []model.Sensor {
	out := make([]model.Sensor, 0, len(names))
	for _, name := range names {
		out = append(out, model.Sensor{
			Name:     name,
			Type:     model.SensorHTTP,
			Target:   "https://" + name,
			Interval: 60,
			Timeout:  10,
			Enabled:  true,
			Origin:   model.OriginExample,
		})
	}
	return out
}

// TestCreateMissingSensorsReportsWhatItWrote covers the ordinary case, and the
// reason the function returns anything at all: the caller synthesises history for
// exactly the sensors it created, so a run that created nothing has to invent
// nothing.
func TestCreateMissingSensorsReportsWhatItWrote(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	created, err := st.CreateMissingSensors(ctx, exampleSensorSet("one", "two"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("created %d sensor(s), want 2", len(created))
	}
	for _, c := range created {
		if c.ID == 0 {
			t.Errorf("%q came back without an id; the caller cannot use it", c.Name)
		}
		if c.Origin != model.OriginExample {
			t.Errorf("%q lost its origin marker on the way in", c.Name)
		}
	}

	// The second run is the second click on the button. It has to be a no-op
	// rather than a duplicate or an error.
	again, err := st.CreateMissingSensors(ctx, exampleSensorSet("one", "two"))
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("the second run created %d sensor(s); it has to create none", len(again))
	}

	all, err := st.ListSensors(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("the table holds %d sensor(s) after two runs, want 2", len(all))
	}
}

// TestCreateMissingSensorsLeavesAnOccupiedNameAlone is the UNIQUE constraint
// showing through, and the reason the collision is a skip rather than an error.
//
// sensors.name is unique. Somebody who made their own sensor called "Example
// website" — or who renamed one to that — must not make the whole restore fail
// with a 500; they must get the four they are missing.
func TestCreateMissingSensorsLeavesAnOccupiedNameAlone(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	own := model.Sensor{
		Name: "Example website", Type: model.SensorHTTP,
		Target: "https://mine.example", Interval: 60, Timeout: 10, Enabled: true,
	}
	mustSensor(t, st, own)

	created, err := st.CreateMissingSensors(ctx, exampleSensorSet("Example website", "Example website TLS"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(created) != 1 || created[0].Name != "Example website TLS" {
		t.Fatalf("created %v, want just the name that was free", created)
	}

	all, err := st.ListSensors(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("the table holds %d sensor(s), want 2", len(all))
	}
	for _, c := range all {
		// The pre-existing row is the person's own; the restore must not have
		// stamped it as example data, which would put it in front of the removal
		// button.
		if c.Name == "Example website" && c.Origin == model.OriginExample {
			t.Error("the sensor that was already there came back marked as example data")
		}
		if c.Name == "Example website" && c.Target != "https://mine.example" {
			t.Errorf("the sensor that was already there was overwritten: target is %q", c.Target)
		}
	}
}

// TestCreateMissingSensorsSerialisesConcurrentCalls is the reason the check and
// the insert share one transaction.
//
// Two administrators clicking at the same moment, or one clicking twice quickly,
// must not both find a name free and write it. The count is the assertion: every
// name is created exactly once across every run, whichever run gets it.
func TestCreateMissingSensorsSerialisesConcurrentCalls(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	const runners = 8
	names := []string{"alpha", "beta", "gamma"}

	var (
		mu      sync.Mutex
		created int
		errs    []error
		wg      sync.WaitGroup
	)
	for range runners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := st.CreateMissingSensors(ctx, exampleSensorSet(names...))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			created += len(out)
		}()
	}
	wg.Wait()

	for _, err := range errs {
		t.Errorf("a concurrent restore failed: %v", err)
	}
	if created != len(names) {
		t.Errorf("%d sensor(s) were reported created across %d runs, want %d — a name was "+
			"written twice or reported twice", created, runners, len(names))
	}
	all, err := st.ListSensors(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != len(names) {
		t.Errorf("the table holds %d sensor(s), want %d", len(all), len(names))
	}
}

// TestDeleteExampleDataTakesTheInventedPlacesWithIt covers the reason sites grew
// an origin column: the example dataset invents places for its sensors, and the
// button that removes the example data must not leave those places behind as
// empty rows nobody can account for.
func TestDeleteExampleDataTakesTheInventedPlacesWithIt(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	lat, lon := 52.52, 13.405
	invented := model.Site{Name: "Homelab", Kind: model.SiteLocal,
		Lat: &lat, Lon: &lon, Origin: model.OriginExample}
	if err := st.CreateSite(ctx, &invented); err != nil {
		t.Fatalf("create the invented site: %v", err)
	}
	// A place the operator made. It has no example sensor on it and is none of the
	// removal's business.
	mine := model.Site{Name: "Office", Kind: model.SiteLocal}
	if err := st.CreateSite(ctx, &mine); err != nil {
		t.Fatalf("create the operator's site: %v", err)
	}

	sensors := exampleSensorSet("Example website")
	sensors[0].Site = "Homelab"
	if _, err := st.CreateMissingSensors(ctx, sensors); err != nil {
		t.Fatalf("create the example sensor: %v", err)
	}

	removed, err := st.DeleteExampleData(ctx)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if removed.Sites != 1 {
		t.Errorf("removed %d site(s), want 1: only the invented one", removed.Sites)
	}
	if _, err := st.GetSite(ctx, "Homelab"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the invented site survived the removal of its sensors: %v", err)
	}
	if _, err := st.GetSite(ctx, "Office"); err != nil {
		t.Errorf("the operator's own empty site was removed: %v", err)
	}
}

// TestDeleteExampleDataKeepsAPlaceSomebodyElseUses is the other half of that
// promise, and the failure it prevents is worse than a leftover row: an example
// site the operator has since filed their own sensor under is a place somebody is
// using, and deleting it would either fail on the foreign key or take their
// sensor's place away from it.
func TestDeleteExampleDataKeepsAPlaceSomebodyElseUses(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	site := model.Site{Name: "Homelab", Kind: model.SiteLocal, Origin: model.OriginExample}
	if err := st.CreateSite(ctx, &site); err != nil {
		t.Fatalf("create the site: %v", err)
	}
	example := exampleSensorSet("Example website")
	example[0].Site = "Homelab"
	if _, err := st.CreateMissingSensors(ctx, example); err != nil {
		t.Fatalf("create the example sensor: %v", err)
	}
	mine := model.Sensor{Name: "My own NAS", Type: model.SensorTCP,
		Target: "192.168.1.10:22", Interval: 60, Timeout: 10, Enabled: true,
		Site: "Homelab"}
	if err := st.CreateSensor(ctx, &mine); err != nil {
		t.Fatalf("create the operator's sensor: %v", err)
	}

	// The button reports what it would take before it takes it, and the number it
	// reports has to be the number of places it actually removes.
	before, err := st.CountExampleData(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if before.Sites != 0 {
		t.Errorf("count says %d site(s) would go, but one of them is in use", before.Sites)
	}

	removed, err := st.DeleteExampleData(ctx)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if removed.Sites != 0 {
		t.Errorf("removed %d site(s), want 0: the place is still in use", removed.Sites)
	}
	if _, err := st.GetSite(ctx, "Homelab"); err != nil {
		t.Fatalf("the site the operator's own sensor sits at was removed: %v", err)
	}
	found, err := st.GetSensorByName(ctx, "My own NAS")
	if err != nil {
		t.Fatalf("the operator's sensor did not survive: %v", err)
	}
	if found.Site != "Homelab" {
		t.Errorf("the operator's sensor now points at %q", found.Site)
	}
}

// TestSiteOriginSurvivesAnEdit pins the reason updateSite takes origin from the
// existing row rather than from the argument it was handed. A save from the
// dashboard builds a fresh Site with no origin on it, and a site that lost its
// marker that way would quietly stop being example data — which is to say, it
// would be left behind by the removal button.
func TestSiteOriginSurvivesAnEdit(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	start := model.Site{Name: "Homelab", Kind: model.SiteLocal, Origin: model.OriginExample}
	if err := st.CreateSite(ctx, &start); err != nil {
		t.Fatalf("create: %v", err)
	}

	lat, lon := 52.52, 13.405
	edited := model.Site{Name: "Homelab", Kind: model.SiteLocal, Lat: &lat, Lon: &lon}
	if err := st.UpdateSite(ctx, "Homelab", &edited); err != nil {
		t.Fatalf("update: %v", err)
	}
	if edited.Origin != model.OriginExample {
		t.Errorf("the site came back with origin %q; the caller answers with this value",
			edited.Origin)
	}
	stored, err := st.GetSite(ctx, "Homelab")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Origin != model.OriginExample {
		t.Errorf("an edit cleared the example marker: origin is now %q", stored.Origin)
	}
	if !stored.Placed() {
		t.Error("the edit did not stick: the coordinates are gone")
	}
}
