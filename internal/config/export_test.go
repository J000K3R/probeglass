package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/j000k3r/probeglass/internal/model"
)

// The file the dashboard writes has to be one this package can read back: the
// point of the export is that the YAML becomes the source of truth, so a field
// the exporter forgets is a field the next apply would clear.
func TestExportedFileReadsBack(t *testing.T) {
	sites := []model.Site{
		{Name: "Rack A", Kind: model.SiteLocal, Icon: "builtin:server"},
	}
	channels := []model.Channel{
		{ID: 7, Name: "telegram", Type: "telegram", Enabled: true,
			Config: map[string]string{"token": "abc", "chat_id": "42"}},
	}
	sensors := []model.Sensor{
		{
			Name: "Router", Type: model.SensorPing, Target: "10.0.0.1",
			Interval: 60, Timeout: 10, Retries: 4, Enabled: true,
			Group: "Netz", Site: "Rack A", Tags: []string{"lan", "core"},
			Icon: "builtin:router", Options: model.Options{"count": 3},
			Channels: []int64{7}, EscalateChannels: []int64{7},
			ReminderEvery: 3600, EscalateAfter: 900,
		},
		{
			// Example data is not the setup somebody wants to version.
			Name: "Example", Type: model.SensorHTTP, Target: "https://example.org",
			Interval: 60, Timeout: 10, Retries: 1, Enabled: true,
			Origin: model.OriginExample,
		},
	}

	data, err := Export(sites, channels, sensors, false)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("the exported file does not parse: %v\n%s", err, data)
	}

	if len(cfg.Sensors) != 1 {
		t.Fatalf("exported %d sensors, want only the non-example one:\n%s", len(cfg.Sensors), data)
	}
	got := cfg.Sensors[0]
	if got.Name != "Router" || got.Type != "ping" || got.Target != "10.0.0.1" {
		t.Errorf("sensor identity = %+v", got)
	}
	if got.Interval.D() != time.Minute || got.Timeout.D() != 10*time.Second {
		t.Errorf("interval/timeout = %v/%v", got.Interval.D(), got.Timeout.D())
	}
	if got.Retries == nil || *got.Retries != 4 {
		t.Errorf("retries = %v, want 4", got.Retries)
	}
	if got.ReminderEvery.D() != time.Hour || got.EscalateAfter.D() != 15*time.Minute {
		t.Errorf("reminder/escalation = %v/%v", got.ReminderEvery.D(), got.EscalateAfter.D())
	}
	if got.Group != "Netz" || got.Site != "Rack A" || got.Icon != "builtin:router" {
		t.Errorf("group/site/icon = %q/%q/%q", got.Group, got.Site, got.Icon)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "lan" {
		t.Errorf("tags = %v", got.Tags)
	}
	if len(got.Channels) != 1 || got.Channels[0] != "telegram" {
		t.Errorf("channels = %v", got.Channels)
	}
	if len(got.EscalateChannels) != 1 || got.EscalateChannels[0] != "telegram" {
		t.Errorf("escalation channels = %v", got.EscalateChannels)
	}
	if n := got.Options.Int("count", 0); n != 3 {
		t.Errorf("options count = %d, want 3", n)
	}

	if len(cfg.Channels) != 1 || cfg.Channels[0].Config["token"] != "abc" {
		t.Errorf("channels did not carry their config: %+v", cfg.Channels)
	}
	if len(cfg.Sites) != 1 || cfg.Sites[0].Name != "Rack A" || cfg.Sites[0].Icon != "builtin:server" {
		t.Errorf("sites = %+v", cfg.Sites)
	}

	// And the same document with the examples asked for has both sensors.
	withExamples, err := Export(sites, channels, sensors, true)
	if err != nil {
		t.Fatalf("export with examples: %v", err)
	}
	var all Config
	if err := yaml.Unmarshal(withExamples, &all); err != nil {
		t.Fatalf("the file with examples does not parse: %v", err)
	}
	if len(all.Sensors) != 2 {
		t.Errorf("exported %d sensors with examples, want 2", len(all.Sensors))
	}
}
