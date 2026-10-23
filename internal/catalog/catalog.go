// Package catalog is the built-in list of device templates: the checks that
// make a sensible first screen for a box somebody has just connected.
//
// A template is a starting point, not a promise. Vendor OIDs move between
// firmware versions, which is why every check becomes an ordinary sensor and can
// be edited afterwards. The numeric OIDs are written out rather than named after a
// MIB because a name would need a MIB file at the device's side of the probe, and
// the sensor deliberately refuses to guess at one.
package catalog

import "github.com/j000k3r/probeglass/internal/model"

// Uptime is the one object every SNMP agent has.
const uptimeOID = "1.3.6.1.2.1.1.3.0"

// Check is one sensor a template proposes.
type Check struct {
	Key      string           `json:"key"`
	LabelKey string           `json:"label_key"`
	Type     model.SensorType `json:"type"`
	// Interval is the suggested probe interval in seconds. Temps and status are
	// cheap and useful at a minute; uptime does not need asking that often.
	Interval int           `json:"interval"`
	Options  model.Options `json:"options,omitempty"`
}

// Template is a vendor (or a family of devices) and the checks it needs.
type Template struct {
	ID             string  `json:"id"`
	Vendor         string  `json:"vendor"`
	Product        string  `json:"product"`
	Icon           string  `json:"icon"`
	NameKey        string  `json:"name_key"`
	DescriptionKey string  `json:"description_key"`
	Checks         []Check `json:"checks"`
}

// Templates returns the catalogue. It is a function rather than a package-level
// variable so a caller cannot append to the list the next caller sees.
func Templates() []Template {
	return []Template{
		{
			ID:             "generic",
			Vendor:         "Generic",
			Product:        "HOST-RESOURCES-MIB",
			Icon:           "builtin:server",
			NameKey:        "catalog.template.generic.name",
			DescriptionKey: "catalog.template.generic.description",
			Checks: []Check{
				{Key: "uptime", LabelKey: "catalog.template.generic.uptime", Type: model.SensorSNMP,
					Interval: 300, Options: model.Options{"oid": uptimeOID, "min": 1}},
				{Key: "cpu_load", LabelKey: "catalog.template.generic.cpu_load", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.2.1.25.3.3.1.2.1", "max": 95}},
			},
		},
		{
			ID:             "qnap",
			Vendor:         "QNAP",
			Product:        "QTS",
			Icon:           "builtin:server",
			NameKey:        "catalog.template.qnap.name",
			DescriptionKey: "catalog.template.qnap.description",
			Checks: []Check{
				// The Ex scalars are integers in °C; the older .1.2 branch returns
				// strings and is not what min/max can judge.
				{Key: "cpu_temp", LabelKey: "catalog.template.qnap.cpu_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.24681.1.3.5.0", "max": 80}},
				{Key: "system_temp", LabelKey: "catalog.template.qnap.system_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.24681.1.3.6.0", "max": 65}},
				{Key: "disk1_temp", LabelKey: "catalog.template.qnap.disk1_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.24681.1.3.1.11.1.3.1", "max": 55}},
				{Key: "uptime", LabelKey: "catalog.template.qnap.uptime", Type: model.SensorSNMP,
					Interval: 300, Options: model.Options{"oid": uptimeOID, "min": 1}},
			},
		},
		{
			ID:             "synology",
			Vendor:         "Synology",
			Product:        "DiskStation",
			Icon:           "builtin:server",
			NameKey:        "catalog.template.synology.name",
			DescriptionKey: "catalog.template.synology.description",
			Checks: []Check{
				{Key: "system_temp", LabelKey: "catalog.template.synology.system_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.6574.1.2.0", "max": 65}},
				{Key: "system_status", LabelKey: "catalog.template.synology.system_status", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.6574.1.1.0", "expect": "1"}},
				{Key: "disk1_temp", LabelKey: "catalog.template.synology.disk1_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.6574.2.1.1.6.1", "max": 55}},
				{Key: "uptime", LabelKey: "catalog.template.synology.uptime", Type: model.SensorSNMP,
					Interval: 300, Options: model.Options{"oid": uptimeOID, "min": 1}},
			},
		},
		{
			ID:             "unifi",
			Vendor:         "Ubiquiti",
			Product:        "UniFi AP",
			Icon:           "builtin:wifi",
			NameKey:        "catalog.template.unifi.name",
			DescriptionKey: "catalog.template.unifi.description",
			Checks: []Check{
				// The UniFi MIB has no CPU, memory or temperature scalars, and not every
				// firmware answers its private uptime object. The standard uptime is on
				// every agent, so that is what the template asks for; reachability is the
				// other honest check. A newer UniFi OS exposes more under UI-MIB, which
				// can be added as sensors by hand.
				{Key: "uptime", LabelKey: "catalog.template.unifi.uptime", Type: model.SensorSNMP,
					Interval: 300, Options: model.Options{"oid": uptimeOID, "min": 1}},
				{Key: "reachable", LabelKey: "catalog.template.unifi.reachable", Type: model.SensorPing,
					Interval: 60},
			},
		},
		{
			ID:             "apc-ups",
			Vendor:         "APC",
			Product:        "UPS",
			Icon:           "builtin:plug",
			NameKey:        "catalog.template.apc.name",
			DescriptionKey: "catalog.template.apc.description",
			Checks: []Check{
				{Key: "battery_charge", LabelKey: "catalog.template.apc.battery_charge", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.318.1.1.1.2.2.1.0", "min": 50}},
				{Key: "battery_temp", LabelKey: "catalog.template.apc.battery_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.318.1.1.1.2.2.2.0", "max": 40}},
				{Key: "output_load", LabelKey: "catalog.template.apc.output_load", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.318.1.1.1.4.2.3.0", "max": 80}},
				{Key: "uptime", LabelKey: "catalog.template.apc.uptime", Type: model.SensorSNMP,
					Interval: 300, Options: model.Options{"oid": uptimeOID, "min": 1}},
			},
		},
	}
}
