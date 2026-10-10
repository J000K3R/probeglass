package store

import (
	"context"
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
