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

import (
	"net/url"
	"strings"

	"github.com/j000k3r/probeglass/internal/model"
)

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
	// Target is a pattern for the sensor's target. {address} is what the person
	// typed and {host} is its host without a scheme or port. Empty means
	// {address}, which is what every single-address SNMP check wants.
	Target string `json:"target,omitempty"`
	// Interval is the suggested probe interval in seconds. Temps and status are
	// cheap and useful at a minute; uptime does not need asking that often.
	Interval int           `json:"interval"`
	Options  model.Options `json:"options,omitempty"`
	Icon     string        `json:"icon,omitempty"`
	// DefaultOff is for a check that only makes sense on some installations,
	// such as a UPS behind a NAS. It is offered unticked rather than left out.
	DefaultOff bool `json:"default_off,omitempty"`
}

// Credential is a value the template needs beyond the address, such as an API
// token. It is merged into the options of the checks that use it.
type Credential struct {
	Key      string `json:"key"`
	LabelKey string `json:"label_key"`
	HelpKey  string `json:"help_key,omitempty"`
	Secret   bool   `json:"secret"`
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
	NameKey        string `json:"name_key"`
	DescriptionKey string `json:"description_key"`
	// Credential is optional: an API token, for example. The apply dialog asks
	// for it and copies it into every check of a type that uses the key.
	Credential *Credential `json:"credential,omitempty"`
	// Discover asks the device what it has after Check device: MOS lists its
	// disks, pools, containers and hardware sensors this way.
	Discover bool    `json:"discover,omitempty"`
	Checks   []Check `json:"checks"`
}

// HasSNMP says whether the template's device check should speak SNMP.
func (t Template) HasSNMP() bool {
	for _, check := range t.Checks {
		if check.Type == model.SensorSNMP {
			return true
		}
	}
	return false
}

// ResolveTarget expands a check's target pattern for one address.
func ResolveTarget(pattern, address string) string {
	if pattern == "" {
		return address
	}
	out := strings.ReplaceAll(pattern, "{address}", CleanAddress(address))
	if strings.Contains(out, "{host}") {
		out = strings.ReplaceAll(out, "{host}", HostOf(address))
	}
	return out
}

// CleanAddress drops a scheme and a path, keeping host and port. A template
// supplies its own scheme and path, so an address typed as a URL must not turn
// "http://{address}/x" into "http://http://host/x".
func CleanAddress(address string) string {
	a := strings.TrimSpace(address)
	a = strings.TrimPrefix(a, "http://")
	a = strings.TrimPrefix(a, "https://")
	if i := strings.IndexByte(a, '/'); i >= 0 {
		a = a[:i]
	}
	return a
}

// HostOf strips a scheme, path and port from an address.
func HostOf(address string) string {
	raw := strings.TrimSpace(address)
	if !strings.Contains(raw, "://") {
		raw = "//" + raw
	}
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return strings.TrimSpace(address)
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
		{
			ID: "mos", Vendor: "MOS", Product: "NAS OS",
			System: "nas", Tags: []string{"storage", "availability", "power"},
			Source: "https://docs.mos-official.net/docs/API/MOS-API-Overview", Version: "0.1",
			Icon:    "builtin:nas",
			NameKey: "catalog.template.mos.name", DescriptionKey: "catalog.template.mos.description",
			Credential: &Credential{
				Key: "bearer_token", LabelKey: "catalog.credential.token",
				HelpKey: "catalog.credential.tokenHelp", Secret: true,
			},
			Discover: true,
			Checks: []Check{
				{Key: "cpu_load", LabelKey: "catalog.template.mos.cpu_load", Type: model.SensorHTTP,
					Target: "http://{address}/api/v1/system/load", Interval: 60, Icon: "builtin:cpu",
					Options: model.Options{"expected_status": "200", "json_path": "cpu.load", "json_max": 95}},
				{Key: "cpu_temp", LabelKey: "catalog.template.mos.cpu_temp", Type: model.SensorHTTP,
					Target: "http://{address}/api/v1/system/load", Interval: 60, Icon: "builtin:thermometer",
					Options: model.Options{"expected_status": "200", "json_path": "temperature.main", "json_max": 85}},
				{Key: "memory", LabelKey: "catalog.template.mos.memory", Type: model.SensorHTTP,
					Target: "http://{address}/api/v1/system/memory", Interval: 60, Icon: "builtin:cpu",
					Options: model.Options{"expected_status": "200", "json_path": "memory.percentage.used", "json_max": 90}},
				{Key: "reachable", LabelKey: "catalog.template.mos.reachable", Type: model.SensorPing,
					Target: "{host}", Interval: 60, Icon: "builtin:wifi"},
			},
		},
		{
			ID: "home-assistant", Vendor: "Home Assistant", Product: "Core / OS",
			System: "home", Tags: []string{"home", "web", "availability"},
			Icon:    "builtin:bulb",
			NameKey: "catalog.template.homeassistant.name", DescriptionKey: "catalog.template.homeassistant.description",
			Checks: []Check{
				{Key: "web", LabelKey: "catalog.check.web", Type: model.SensorHTTP,
					Target: "http://{address}:8123/", Interval: 60,
					Options: model.Options{"expected_status": "200-399"}},
				{Key: "reachable", LabelKey: "catalog.check.reachable", Type: model.SensorPing,
					Target: "{host}", Interval: 60},
			},
		},
		{
			ID: "pi-hole", Vendor: "Pi-hole", Product: "DNS",
			System: "dns", Tags: []string{"dns", "web", "availability"},
			Icon:    "builtin:lock",
			NameKey: "catalog.template.pihole.name", DescriptionKey: "catalog.template.pihole.description",
			Checks: []Check{
				{Key: "web", LabelKey: "catalog.check.web", Type: model.SensorHTTP,
					Target: "http://{address}/admin/", Interval: 60,
					Options: model.Options{"expected_status": "200-399"}},
				{Key: "reachable", LabelKey: "catalog.check.reachable", Type: model.SensorPing,
					Target: "{host}", Interval: 60},
			},
		},
		{
			ID: "adguard", Vendor: "AdGuard", Product: "Home",
			System: "dns", Tags: []string{"dns", "web", "availability"},
			Icon:    "builtin:lock",
			NameKey: "catalog.template.adguard.name", DescriptionKey: "catalog.template.adguard.description",
			Checks: []Check{
				{Key: "web", LabelKey: "catalog.check.web", Type: model.SensorHTTP,
					Target: "http://{address}/", Interval: 60,
					Options: model.Options{"expected_status": "200-399"}},
				{Key: "reachable", LabelKey: "catalog.check.reachable", Type: model.SensorPing,
					Target: "{host}", Interval: 60},
			},
		},
		{
			ID: "grafana", Vendor: "Grafana", Product: "Labs",
			System: "application", Tags: []string{"web", "availability"},
			Icon:    "builtin:monitor",
			NameKey: "catalog.template.grafana.name", DescriptionKey: "catalog.template.grafana.description",
			Checks: []Check{
				{Key: "health", LabelKey: "catalog.check.health", Type: model.SensorHTTP,
					Target: "http://{address}:3000/api/health", Interval: 60,
					Options: model.Options{"expected_status": "200"}},
				{Key: "reachable", LabelKey: "catalog.check.reachable", Type: model.SensorPing,
					Target: "{host}", Interval: 60},
			},
		},
		{
			ID: "portainer", Vendor: "Portainer", Product: "CE",
			System: "application", Tags: []string{"web", "availability"},
			Icon:    "builtin:container",
			NameKey: "catalog.template.portainer.name", DescriptionKey: "catalog.template.portainer.description",
			Checks: []Check{
				{Key: "web", LabelKey: "catalog.check.web", Type: model.SensorHTTP,
					Target: "http://{address}:9000/", Interval: 60,
					Options: model.Options{"expected_status": "200-399"}},
				{Key: "reachable", LabelKey: "catalog.check.reachable", Type: model.SensorPing,
					Target: "{host}", Interval: 60},
			},
		},
		{
			ID: "jellyfin", Vendor: "Jellyfin", Product: "Media Server",
			System: "media", Tags: []string{"media", "web", "availability"},
			Icon:    "builtin:monitor",
			NameKey: "catalog.template.jellyfin.name", DescriptionKey: "catalog.template.jellyfin.description",
			Checks: []Check{
				{Key: "health", LabelKey: "catalog.check.health", Type: model.SensorHTTP,
					Target: "http://{address}:8096/health", Interval: 60,
					Options: model.Options{"expected_status": "200"}},
				{Key: "reachable", LabelKey: "catalog.check.reachable", Type: model.SensorPing,
					Target: "{host}", Interval: 60},
			},
		},
		{
			ID: "uptime-kuma", Vendor: "Uptime Kuma", Product: "Monitor",
			System: "application", Tags: []string{"web", "availability"},
			Icon:    "builtin:monitor",
			NameKey: "catalog.template.uptimekuma.name", DescriptionKey: "catalog.template.uptimekuma.description",
			Checks: []Check{
				{Key: "web", LabelKey: "catalog.check.web", Type: model.SensorHTTP,
					Target: "http://{address}:3001/", Interval: 60,
					Options: model.Options{"expected_status": "200-399"}},
				{Key: "reachable", LabelKey: "catalog.check.reachable", Type: model.SensorPing,
					Target: "{host}", Interval: 60},
			},
		},
		{
			ID: "node-red", Vendor: "Node-RED", Product: "Flow editor",
			System: "home", Tags: []string{"home", "web", "availability"},
			Icon:    "builtin:bulb",
			NameKey: "catalog.template.nodered.name", DescriptionKey: "catalog.template.nodered.description",
			Checks: []Check{
				{Key: "web", LabelKey: "catalog.check.web", Type: model.SensorHTTP,
					Target: "http://{address}:1880/", Interval: 60,
					Options: model.Options{"expected_status": "200-399"}},
				{Key: "reachable", LabelKey: "catalog.check.reachable", Type: model.SensorPing,
					Target: "{host}", Interval: 60},
			},
		},
		{
			ID: "mosquitto", Vendor: "Eclipse", Product: "Mosquitto", //nolint:misspell // the MQTT broker spells it with two t
			System: "application", Tags: []string{"network", "availability"},
			Icon:    "builtin:plug",
			NameKey: "catalog.template.mosquitto.name", DescriptionKey: "catalog.template.mosquitto.description",
			Checks: []Check{
				{Key: "port", LabelKey: "catalog.check.port", Type: model.SensorTCP,
					Target: "{host}:1883", Interval: 60},
				{Key: "reachable", LabelKey: "catalog.check.reachable", Type: model.SensorPing,
					Target: "{host}", Interval: 60},
			},
		},
		{
			ID: "shelly", Vendor: "Shelly", Product: "Relay",
			System: "home", Tags: []string{"home", "web", "availability"},
			Icon:    "builtin:plug",
			NameKey: "catalog.template.shelly.name", DescriptionKey: "catalog.template.shelly.description",
			Checks: []Check{
				{Key: "web", LabelKey: "catalog.check.web", Type: model.SensorHTTP,
					Target: "http://{address}/status", Interval: 60,
					Options: model.Options{"expected_status": "200"}},
				{Key: "reachable", LabelKey: "catalog.check.reachable", Type: model.SensorPing,
					Target: "{host}", Interval: 60},
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
