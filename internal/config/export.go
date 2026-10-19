package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/j000k3r/probeglass/internal/model"
)

// ExportDoc is the YAML document the dashboard writes: the three things a
// configuration file can declare, and nothing that is not one of them.
//
// Accounts, incidents and history are deliberately absent. They are state, not
// configuration; the JSON backup is the document for moving an installation, and
// this one is for turning a dashboard-made setup into a file somebody can
// version and maintain.
type ExportDoc struct {
	Sites    []SiteSpec     `yaml:"sites,omitempty"`
	Channels []ChannelSpec  `yaml:"channels,omitempty"`
	Sensors  []ExportSensor `yaml:"sensors,omitempty"`
}

// ExportSensor is SensorSpec with its durations written as strings, so that the
// file reads the way a hand-written one does instead of carrying nanoseconds.
type ExportSensor struct {
	Name       string        `yaml:"name"`
	Type       string        `yaml:"type"`
	Target     string        `yaml:"target"`
	Interval   string        `yaml:"interval,omitempty"`
	Timeout    string        `yaml:"timeout,omitempty"`
	Retries    *int          `yaml:"retries,omitempty"`
	Enabled    *bool         `yaml:"enabled,omitempty"`
	Group      string        `yaml:"group,omitempty"`
	Site       string        `yaml:"site,omitempty"`
	DependsOn  string        `yaml:"depends_on,omitempty"`
	Tags       []string      `yaml:"tags,omitempty"`
	Reminder   string        `yaml:"reminder_every,omitempty"`
	Escalate   string        `yaml:"escalate_after,omitempty"`
	Options    model.Options `yaml:"options,omitempty"`
	Channels   []string      `yaml:"channels,omitempty"`
	Escalation []string      `yaml:"escalate_channels,omitempty"`
	Icon       string        `yaml:"icon,omitempty"`
}

// Export renders the installation as a configuration file.
//
// Example data is left out unless includeExamples is set. It was invented so
// that a first dashboard is not empty, and a file that turned it into managed
// sensors would be versioning somebody else's sample.
//
// Channel names are resolved through the channels list; an assignment to a
// channel that no longer exists cannot be expressed in the file and is left out.
// The API masks secrets in what it sends to a browser, but this reads the store
// directly: a configuration file without the tokens would be a file that applies
// and then alerts nowhere.
func Export(sites []model.Site, channels []model.Channel, sensors []model.Sensor, includeExamples bool) ([]byte, error) {
	names := make(map[int64]string, len(channels))
	doc := ExportDoc{}
	for _, ch := range channels {
		names[ch.ID] = ch.Name
		enabled := ch.Enabled
		doc.Channels = append(doc.Channels, ChannelSpec{
			Name:    ch.Name,
			Type:    ch.Type,
			Enabled: boolPointer(!enabled, &enabled),
			Config:  ch.Config,
		})
	}
	for _, site := range sites {
		if !includeExamples && site.Origin == model.OriginExample {
			continue
		}
		doc.Sites = append(doc.Sites, SiteSpec{
			Name: site.Name,
			Kind: string(site.Kind),
			Lat:  site.Lat,
			Lon:  site.Lon,
			Icon: site.Icon,
		})
	}
	for _, c := range sensors {
		if !includeExamples && c.Origin == model.OriginExample {
			continue
		}
		doc.Sensors = append(doc.Sensors, exportSensor(c, names))
	}

	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("render the configuration file: %w", err)
	}
	header := "# ProbeGlass configuration, written from the running installation.\n" +
		"# It carries sites, channels and sensors; accounts, incidents and history\n" +
		"# are not part of this file. Secrets in a channel's config are in clear text.\n"
	return append([]byte(header), body...), nil
}

func exportSensor(c model.Sensor, names map[int64]string) ExportSensor {
	s := ExportSensor{
		Name:      c.Name,
		Type:      string(c.Type),
		Target:    c.Target,
		Interval:  exportDuration(c.Interval),
		Timeout:   exportDuration(c.Timeout),
		Group:     c.Group,
		Site:      c.Site,
		DependsOn: c.Parent,
		Tags:      c.Tags,
		Reminder:  exportDuration(c.ReminderEvery),
		Escalate:  exportDuration(c.EscalateAfter),
		Options:   c.Options,
		Icon:      c.Icon,
	}
	if c.Retries != 1 {
		retries := c.Retries
		s.Retries = &retries
	}
	if !c.Enabled {
		disabled := false
		s.Enabled = &disabled
	}
	for _, id := range c.Channels {
		if name, ok := names[id]; ok {
			s.Channels = append(s.Channels, name)
		}
	}
	for _, id := range c.EscalateChannels {
		if name, ok := names[id]; ok {
			s.Escalation = append(s.Escalation, name)
		}
	}
	return s
}

// exportDuration writes seconds the way the loader reads them: the largest unit
// that divides the value exactly.
func exportDuration(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	d := time.Duration(seconds) * time.Second
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// boolPointer returns value when the file needs to say it, and nil when the
// default already says it.
func boolPointer(needed bool, value *bool) *bool {
	if !needed {
		return nil
	}
	v := *value
	return &v
}
