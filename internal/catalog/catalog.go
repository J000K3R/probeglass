// Package catalog is the built-in list of device templates: the checks that make
// a sensible first screen for a box somebody has just connected.
//
// A template is a starting point, not a promise. Vendor OIDs move between
// firmware versions, which is why every check becomes an ordinary sensor and can
// be edited afterwards. The numeric OIDs are written out rather than named after a
// MIB because a name would need a MIB file at the device's side of the probe, and
// the sensor deliberately refuses to guess at one.
//
// Some vendor OIDs were cross-checked against the PRTG Device Templates, which are
// BSD-3-Clause, Copyright (c) 2014 Paessler AG. Only the OID numbers were used; no
// PRTG code or template file is part of this package.
package catalog

import "github.com/j000k3r/probeglass/internal/model"

// Uptime is the one object every SNMP agent has.
const uptimeOID = "1.3.6.1.2.1.1.3.0"

// cpuLoadOID is hrProcessorLoad for the first processor, from HOST-RESOURCES-MIB.
// It is what a Linux server or a NAS without a vendor MIB can still answer.
const cpuLoadOID = "1.3.6.1.2.1.25.3.3.1.2.1"

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
	ID      string `json:"id"`
	Vendor  string `json:"vendor"`
	Product string `json:"product"`
	// System is the catalogue category (nas, server, router, firewall, wireless,
	// ups, printer), and Tags are the things it watches (availability, cpu,
	// temperature, storage, power). Both are keys the interface translates.
	System string   `json:"system"`
	Tags   []string `json:"tags"`
	// Source points at the MIB or template the OIDs were checked against, when
	// there is one. It is shown in the template's detail view.
	Source  string `json:"source,omitempty"`
	Version string `json:"version,omitempty"`
	Icon    string `json:"icon"`
	// NameKey and DescriptionKey are translated; vendor and product are names.
	NameKey        string  `json:"name_key"`
	DescriptionKey string  `json:"description_key"`
	Checks         []Check `json:"checks"`
}

// Templates returns the catalogue. It is a function rather than a package-level
// variable so a caller cannot append to the list the next caller sees.
func Templates() []Template {
	return []Template{
		{
			ID: "generic", Vendor: "Generic", Product: "HOST-RESOURCES-MIB",
			System: "server", Tags: []string{"availability", "cpu"},
			Icon:    "builtin:server",
			NameKey: "catalog.template.generic.name", DescriptionKey: "catalog.template.generic.description",
			Checks: []Check{
				{Key: "uptime", LabelKey: "catalog.template.generic.uptime", Type: model.SensorSNMP,
					Interval: 300, Options: model.Options{"oid": uptimeOID, "min": 1}},
				{Key: "cpu_load", LabelKey: "catalog.template.generic.cpu_load", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": cpuLoadOID, "max": 95}},
			},
		},
		{
			ID: "qnap", Vendor: "QNAP", Product: "QTS",
			System: "nas", Tags: []string{"storage", "temperature", "availability"},
			Source: "https://gitlab.com/PRTG/Device-Templates/qnap", Version: "0.1",
			Icon:    "builtin:nas",
			NameKey: "catalog.template.qnap.name", DescriptionKey: "catalog.template.qnap.description",
			Checks: []Check{
				// The Ex scalars are integers in °C; the older .1.2 branch returns
				// strings and is not what min/max can judge.
				{Key: "cpu_temp", LabelKey: "catalog.template.qnap.cpu_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.24681.1.3.5.0", "max": 80}},
				{Key: "system_temp", LabelKey: "catalog.template.qnap.system_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.24681.1.3.6.0", "max": 65}},
				{Key: "disk1_temp", LabelKey: "catalog.template.qnap.disk1_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.24681.1.3.11.1.3.1", "max": 55}},
				{Key: "disk2_temp", LabelKey: "catalog.template.qnap.disk2_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.24681.1.3.11.1.3.2", "max": 55}},
				{Key: "disk3_temp", LabelKey: "catalog.template.qnap.disk3_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.24681.1.3.11.1.3.3", "max": 55}},
				{Key: "disk4_temp", LabelKey: "catalog.template.qnap.disk4_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.24681.1.3.11.1.3.4", "max": 55}},
				{Key: "uptime", LabelKey: "catalog.template.qnap.uptime", Type: model.SensorSNMP,
					Interval: 300, Options: model.Options{"oid": uptimeOID, "min": 1}},
			},
		},
		{
			ID: "synology", Vendor: "Synology", Product: "DiskStation",
			System: "nas", Tags: []string{"storage", "temperature", "availability"},
			Source: "https://kb.synology.com/en-global/DG/Synology_DiskStation_MIB_Guide/3", Version: "0.1",
			Icon:    "builtin:nas",
			NameKey: "catalog.template.synology.name", DescriptionKey: "catalog.template.synology.description",
			Checks: []Check{
				{Key: "system_temp", LabelKey: "catalog.template.synology.system_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.6574.1.2.0", "max": 65}},
				{Key: "system_status", LabelKey: "catalog.template.synology.system_status", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.6574.1.1.0", "expect": "1"}},
				{Key: "disk1_temp", LabelKey: "catalog.template.synology.disk1_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.6574.2.1.1.6.1", "max": 55}},
				{Key: "disk2_temp", LabelKey: "catalog.template.synology.disk2_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.6574.2.1.1.6.2", "max": 55}},
				{Key: "disk3_temp", LabelKey: "catalog.template.synology.disk3_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.6574.2.1.1.6.3", "max": 55}},
				{Key: "disk4_temp", LabelKey: "catalog.template.synology.disk4_temp", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.6574.2.1.1.6.4", "max": 55}},
				{Key: "uptime", LabelKey: "catalog.template.synology.uptime", Type: model.SensorSNMP,
					Interval: 300, Options: model.Options{"oid": uptimeOID, "min": 1}},
			},
		},
		{
			ID: "unifi", Vendor: "Ubiquiti", Product: "UniFi AP",
			System: "wireless", Tags: []string{"availability"},
			Source:  "https://help.ui.com/hc/en-us/articles/33502980942615-SNMP-Monitoring-in-UniFi-Network",
			Icon:    "builtin:wifi",
			NameKey: "catalog.template.unifi.name", DescriptionKey: "catalog.template.unifi.description",
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
			ID: "apc-ups", Vendor: "APC", Product: "UPS",
			System: "ups", Tags: []string{"power", "availability"},
			Source: "https://www.apc.com/us/en/faqs/FA156435/", Version: "0.1",
			Icon:    "builtin:battery",
			NameKey: "catalog.template.apc.name", DescriptionKey: "catalog.template.apc.description",
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
		{
			ID: "mikrotik", Vendor: "MikroTik", Product: "RouterOS",
			System: "router", Tags: []string{"cpu", "temperature", "availability"},
			Source: "https://gitlab.com/PRTG/Device-Templates/MicroTik", Version: "0.1",
			Icon:    "builtin:router",
			NameKey: "catalog.template.mikrotik.name", DescriptionKey: "catalog.template.mikrotik.description",
			Checks: []Check{
				{Key: "cpu_load", LabelKey: "catalog.template.mikrotik.cpu_load", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.14988.1.1.3.14.0", "max": 95}},
				{Key: "temperature", LabelKey: "catalog.template.mikrotik.temperature", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": "1.3.6.1.4.1.14988.1.1.3.10.0", "max": 85}},
				{Key: "uptime", LabelKey: "catalog.template.mikrotik.uptime", Type: model.SensorSNMP,
					Interval: 300, Options: model.Options{"oid": uptimeOID, "min": 1}},
				{Key: "reachable", LabelKey: "catalog.template.mikrotik.reachable", Type: model.SensorPing,
					Interval: 60},
			},
		},
		{
			ID: "pfsense", Vendor: "pfSense", Product: "CE / Plus",
			System: "firewall", Tags: []string{"cpu", "availability"},
			Icon:    "builtin:lock",
			NameKey: "catalog.template.pfsense.name", DescriptionKey: "catalog.template.pfsense.description",
			Checks: []Check{
				{Key: "cpu_load", LabelKey: "catalog.template.pfsense.cpu_load", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": cpuLoadOID, "max": 95}},
				{Key: "uptime", LabelKey: "catalog.template.pfsense.uptime", Type: model.SensorSNMP,
					Interval: 300, Options: model.Options{"oid": uptimeOID, "min": 1}},
				{Key: "reachable", LabelKey: "catalog.template.pfsense.reachable", Type: model.SensorPing,
					Interval: 60},
			},
		},
		{
			ID: "proxmox", Vendor: "Proxmox", Product: "VE",
			System: "server", Tags: []string{"cpu", "availability"},
			Icon:    "builtin:server",
			NameKey: "catalog.template.proxmox.name", DescriptionKey: "catalog.template.proxmox.description",
			Checks: []Check{
				{Key: "cpu_load", LabelKey: "catalog.template.proxmox.cpu_load", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": cpuLoadOID, "max": 95}},
				{Key: "uptime", LabelKey: "catalog.template.proxmox.uptime", Type: model.SensorSNMP,
					Interval: 300, Options: model.Options{"oid": uptimeOID, "min": 1}},
				{Key: "reachable", LabelKey: "catalog.template.proxmox.reachable", Type: model.SensorPing,
					Interval: 60},
			},
		},
		{
			ID: "truenas", Vendor: "TrueNAS", Product: "CORE / SCALE",
			System: "nas", Tags: []string{"cpu", "availability"},
			Icon:    "builtin:nas",
			NameKey: "catalog.template.truenas.name", DescriptionKey: "catalog.template.truenas.description",
			Checks: []Check{
				{Key: "cpu_load", LabelKey: "catalog.template.truenas.cpu_load", Type: model.SensorSNMP,
					Interval: 60, Options: model.Options{"oid": cpuLoadOID, "max": 95}},
				{Key: "uptime", LabelKey: "catalog.template.truenas.uptime", Type: model.SensorSNMP,
					Interval: 300, Options: model.Options{"oid": uptimeOID, "min": 1}},
				{Key: "reachable", LabelKey: "catalog.template.truenas.reachable", Type: model.SensorPing,
					Interval: 60},
			},
		},
		{
			ID: "printer", Vendor: "Generic", Product: "Printer MIB",
			System: "printer", Tags: []string{"availability"},
			Icon:    "builtin:printer",
			NameKey: "catalog.template.printer.name", DescriptionKey: "catalog.template.printer.description",
			Checks: []Check{
				{Key: "page_count", LabelKey: "catalog.template.printer.page_count", Type: model.SensorSNMP,
					Interval: 300, Options: model.Options{"oid": "1.3.6.1.2.1.43.10.2.1.4.1.1", "min": 1}},
				{Key: "uptime", LabelKey: "catalog.template.printer.uptime", Type: model.SensorSNMP,
					Interval: 300, Options: model.Options{"oid": uptimeOID, "min": 1}},
				{Key: "reachable", LabelKey: "catalog.template.printer.reachable", Type: model.SensorPing,
					Interval: 60},
			},
		},
	}
}

// Find returns the template with the given id.
func Find(id string) (Template, bool) {
	for _, template := range Templates() {
		if template.ID == id {
			return template, true
		}
	}
	return Template{}, false
}
