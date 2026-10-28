// Package scan looks at a private IPv4 range and suggests sensors for what it
// finds. It is deliberately narrow: a fixed port list, a bounded host count, and
// only addresses the RFC reserves for private networks. It is not a port scanner
// for the internet.
package scan

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/j000k3r/probeglass/internal/catalog"
	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/sensor"
)

// Ports is the whole of what a scan dials. A fixed list is the difference between
// a homelab inventory and a port scanner: the ports are the ones the templates in
// this program already know how to watch.
var Ports = []int{
	22, 25, 53, 80, 81, 443, 445, 631, 1883, 2283, 3000, 32400, 4533, 5000,
	5001, 6052, 6767, 7878, 8000, 8006, 8080, 8086, 8096, 8123, 8181, 8384,
	8443, 8581, 8686, 8989, 9000, 9090, 9091, 9696, 13378, 19999,
}

// Suggestion is one sensor the scan proposes, in the same shape the catalogue
// uses so the dialog can create it without a second code path.
type Suggestion struct {
	Key      string           `json:"key"`
	Label    string           `json:"label,omitempty"`
	LabelKey string           `json:"label_key,omitempty"`
	Type     model.SensorType `json:"type"`
	Target   string           `json:"target"`
	Interval int              `json:"interval"`
	Options  model.Options    `json:"options,omitempty"`
	Icon     string           `json:"icon,omitempty"`
}

// Host is one address that answered.
type Host struct {
	IP          string       `json:"ip"`
	SysDescr    string       `json:"sys_descr,omitempty"`
	SysObjectID string       `json:"sys_object_id,omitempty"`
	Ports       []int        `json:"ports"`
	TemplateIDs []string     `json:"template_ids,omitempty"`
	Suggestions []Suggestion `json:"suggestions"`
}

// ValidateCIDR returns the addresses to scan. Only private, loopback and
// link-local IPv4 ranges are accepted, and never more than 256 addresses.
func ValidateCIDR(cidr string) ([]net.IP, error) {
	raw := strings.TrimSpace(cidr)
	if raw == "" {
		return nil, fmt.Errorf("a network such as 192.168.1.0/24 is required")
	}
	if !strings.Contains(raw, "/") {
		raw += "/32"
	}
	ip, network, err := net.ParseCIDR(raw)
	if err != nil || ip.To4() == nil {
		return nil, fmt.Errorf("only IPv4 networks are supported, got %q", cidr)
	}
	ones, bits := network.Mask.Size()
	if bits != 32 {
		return nil, fmt.Errorf("only IPv4 is supported, got %q", cidr)
	}
	if ones < 24 {
		return nil, fmt.Errorf("scan at most 256 addresses: use /24 or smaller, got /%d", ones)
	}
	if !network.IP.IsPrivate() && !network.IP.IsLoopback() && !network.IP.IsLinkLocalUnicast() {
		return nil, fmt.Errorf("only private, loopback or link-local networks can be scanned, got %q", cidr)
	}

	var ips []net.IP
	for v := ipToUint(network.IP); v <= ipToUint(lastAddress(network)); v++ {
		candidate := uintToIP(v)
		// The network and broadcast addresses are not hosts, except in the two
		// tiny prefixes where there is no broadcast.
		if ones <= 30 && (v == ipToUint(network.IP) || v == ipToUint(lastAddress(network))) {
			continue
		}
		ips = append(ips, candidate)
	}
	return ips, nil
}

func lastAddress(n *net.IPNet) net.IP {
	ip := n.IP.To4()
	mask := n.Mask
	last := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		last[i] = ip[i] | ^mask[i]
	}
	return last
}

func ipToUint(ip net.IP) uint32 {
	v := ip.To4()
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}

func uintToIP(v uint32) net.IP {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).To4() //nolint:gosec // G115: each shift leaves exactly one byte
}

// Scan dials every address and returns the ones that answered, with a suggestion
// for each. SNMP is optional: with no credentials the scan is TCP only.
func Scan(ctx context.Context, cidr string, snmpOptions model.Options) ([]Host, []string, error) {
	ips, err := ValidateCIDR(cidr)
	if err != nil {
		return nil, nil, err
	}
	var notes []string
	hasSNMP := len(snmpOptions) > 0

	results := make([]Host, len(ips))
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup
	for i, ip := range ips {
		wg.Add(1)
		go func(index int, address net.IP) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			host := Host{IP: address.String()}
			host.Ports = scanPorts(ctx, address, Ports)
			if hasSNMP {
				snmpCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				found, err := sensor.DiscoverSNMP(snmpCtx, address.String(), snmpOptions)
				cancel()
				if err == nil {
					host.SysDescr = found.SysDescr
					host.SysObjectID = found.SysObjectID
				}
			}
			if len(host.Ports) == 0 && host.SysDescr == "" {
				return
			}
			host.TemplateIDs = templateIDs(host)
			host.Suggestions = suggestions(host, snmpOptions)
			results[index] = host
		}(i, ip)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, notes, err
	}

	hosts := make([]Host, 0, len(results))
	for _, host := range results {
		if host.IP != "" {
			hosts = append(hosts, host)
		}
	}
	sort.Slice(hosts, func(i, j int) bool { return ipLess(hosts[i].IP, hosts[j].IP) })
	return hosts, notes, nil
}

func ipLess(a, b string) bool {
	return ipToUint(net.ParseIP(a)) < ipToUint(net.ParseIP(b))
}

func scanPorts(ctx context.Context, ip net.IP, ports []int) []int {
	var found []int
	var mu sync.Mutex
	var wg sync.WaitGroup
	limit := make(chan struct{}, 10)
	for _, port := range ports {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-limit }()
			dialer := net.Dialer{Timeout: 350 * time.Millisecond}
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(port)))
			if err != nil {
				return
			}
			_ = conn.Close()
			mu.Lock()
			found = append(found, port)
			mu.Unlock()
		}(port)
	}
	wg.Wait()
	sort.Ints(found)
	return found
}

// templateIDs maps what the host says and the ports it opened to the templates
// that already know that product. It is a hint, not an action.
func templateIDs(host Host) []string {
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	descr := strings.ToLower(host.SysDescr)
	switch {
	case strings.Contains(descr, "qnap"):
		add("qnap")
	case strings.Contains(descr, "synology"):
		add("synology")
	case strings.Contains(descr, "unifi"), strings.Contains(descr, "ubiquiti"):
		add("unifi")
	case strings.Contains(descr, "mikrotik"), strings.Contains(descr, "routeros"): //nolint:misspell // RouterOS is MikroTik's operating system
		add("mikrotik")
	case strings.Contains(descr, "proxmox"):
		add("proxmox")
	case strings.Contains(descr, "truenas"):
		add("truenas")
	case strings.Contains(descr, "apc"):
		add("apc-ups")
	case strings.Contains(descr, "printer"), strings.Contains(descr, "brother"), strings.Contains(descr, "hewlett"):
		add("printer")
	}
	byPort := map[int]string{
		81: "nginxproxymanager", 2283: "immich", 4533: "navidrome", 6052: "esphome",
		6767: "bazarr", 7878: "radarr", 8096: "jellyfin", 8123: "home-assistant",
		8181: "tautulli", 8384: "syncthing", 8581: "homebridge", 8686: "lidarr",
		8989: "sonarr", 9000: "portainer", 9090: "prometheus", 9091: "transmission",
		9696: "prowlarr", 13378: "audiobookshelf", 19999: "netdata", 8006: "proxmox",
	}
	for _, port := range host.Ports {
		if id := byPort[port]; id != "" {
			add(id)
		}
	}
	return ids
}

// suggestions is what the dialog offers for one host: reachability, the web
// services it can see, and the SNMP checks the matching template would add.
func suggestions(host Host, snmpOptions model.Options) []Suggestion {
	var out []Suggestion
	out = append(out, Suggestion{
		Key: "ping", LabelKey: "catalog.check.reachable", Type: model.SensorPing,
		Target: host.IP, Interval: 60,
	})
	webPorts := map[int]bool{80: true, 81: true, 443: true, 2283: true, 3000: true, 32400: true,
		4533: true, 5000: true, 5001: true, 6052: true, 8000: true, 8006: true, 8080: true,
		8086: true, 8096: true, 8123: true, 8181: true, 8384: true, 8443: true, 8581: true,
		9000: true, 9090: true, 9091: true, 13378: true, 19999: true}
	for _, port := range host.Ports {
		if !webPorts[port] {
			continue
		}
		secure := port == 443 || port == 8443
		scheme := "http"
		if secure {
			scheme = "https"
		}
		out = append(out, Suggestion{
			Key:   "http-" + strconv.Itoa(port),
			Label: fmt.Sprintf("%s on %d", strings.ToUpper(scheme), port),
			Type:  model.SensorHTTP, Target: fmt.Sprintf("%s://%s/", scheme, net.JoinHostPort(host.IP, strconv.Itoa(port))),
			Interval: 60,
			Options:  model.Options{"expected_status": "200-399", "insecure_skip_verify": secure},
		})
	}
	if len(host.Ports) > 0 && contains(host.Ports, 443) {
		out = append(out, Suggestion{
			Key: "tls-443", Label: "TLS certificate", Type: model.SensorTLS,
			Target: net.JoinHostPort(host.IP, "443"), Interval: 3600,
			Options: model.Options{"cert_warn_days": 14, "insecure_skip_verify": true},
		})
	}

	// The SNMP checks: the generic uptime always, and the matched template's own
	// checks when the device said what it is. The scan's credentials are merged
	// into every one, because that is what made the device answer.
	if host.SysDescr != "" {
		uptime := model.Options{"oid": "1.3.6.1.2.1.1.3.0", "min": 1}
		for k, v := range snmpOptions {
			uptime[k] = v
		}
		out = append(out, Suggestion{
			Key: "snmp-uptime", LabelKey: "catalog.check.uptime", Type: model.SensorSNMP,
			Target: host.IP, Interval: 300, Options: uptime,
		})
		for _, id := range host.TemplateIDs {
			template, ok := catalog.Find(id)
			if !ok {
				continue
			}
			for _, check := range template.Checks {
				if check.Type != model.SensorSNMP {
					continue
				}
				options := model.Options{}
				for k, v := range check.Options {
					options[k] = v
				}
				for k, v := range snmpOptions {
					options[k] = v
				}
				out = append(out, Suggestion{
					Key: id + "." + check.Key, LabelKey: check.LabelKey, Type: check.Type,
					Target: catalog.ResolveTarget(check.Target, host.IP), Interval: check.Interval,
					Options: options, Icon: check.Icon,
				})
			}
		}
	}
	return out
}

func contains(values []int, want int) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
