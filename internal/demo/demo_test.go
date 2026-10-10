package demo

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/j000k3r/weatherglass/internal/model"
	"github.com/j000k3r/weatherglass/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "demo.db")
	st, err := store.Open(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestSensorsAreUsableAndMarked is the contract the rest of the product leans
// on: everything this package hands out is a real sensor, and every one of them
// carries the marker the removal button keys on. A missing marker would leave an
// invented sensor that nothing can take out again.
func TestSensorsAreUsableAndMarked(t *testing.T) {
	list := Sensors()
	if len(list) == 0 {
		t.Fatal("the dataset is empty")
	}
	for _, c := range list {
		if c.Name == "" || c.Target == "" {
			t.Errorf("a sensor is missing its name or target: %+v", c)
		}
		if !c.Type.Valid() {
			t.Errorf("%q has the unknown type %q", c.Name, c.Type)
		}
		if c.Origin != model.OriginExample {
			t.Errorf("%q is not marked as example data", c.Name)
		}
		if c.Interval <= 0 {
			t.Errorf("%q has no interval, so it would never be probed", c.Name)
		}
	}
}

// TestSensorsCopiesTheOptions guards the one mutable thing on the value: the
// options map. A caller that edits what it was given must not be able to reach
// into the package-level table, or the second call would write something else
// than the first.
func TestSensorsCopiesTheOptions(t *testing.T) {
	first := Sensors()
	touched := false
	for i := range first {
		if first[i].Options == nil {
			continue
		}
		for k := range first[i].Options {
			first[i].Options[k] = "clobbered"
			touched = true
			break
		}
		if touched {
			break
		}
	}
	if !touched {
		t.Skip("no sensor in the dataset has options to touch")
	}

	second := Sensors()
	for _, c := range second {
		for k, v := range c.Options {
			if v == "clobbered" {
				t.Fatalf("editing a returned sensor changed the table: %s.%s = %v", c.Name, k, v)
			}
		}
	}
}

// TestSeedCreatesSensorsAndHistory is the whole package in one call, and the
// second call is the half that matters: the Settings button may be pressed
// twice, and the second press must be free.
func TestSeedCreatesSensorsAndHistory(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	res, err := Seed(ctx, st, 7)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if res.Sensors != len(Sensors()) {
		t.Errorf("created %d sensor(s), want the whole dataset (%d)", res.Sensors, len(Sensors()))
	}
	if res.Samples == 0 {
		t.Error("no history was invented; the dataset is supposed to arrive with a week of it")
	}

	counts, err := st.CountExampleData(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts.Sensors != res.Sensors {
		t.Errorf("the table holds %d example sensor(s), the seed reported %d",
			counts.Sensors, res.Sensors)
	}
	if counts.Results != res.Samples {
		t.Errorf("the table holds %d result(s), the seed reported %d",
			counts.Results, res.Samples)
	}
	if counts.Incidents == 0 {
		t.Error("no incidents were written; the outages in the dataset are what the " +
			"incident log is meant to show")
	}

	again, err := Seed(ctx, st, 7)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if again.Sensors != 0 || again.Samples != 0 {
		t.Errorf("the second run wrote %+v; it has to write nothing", again)
	}
}
