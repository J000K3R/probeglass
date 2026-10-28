package scan

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

func TestValidateCIDRAcceptsOnlySmallPrivateNetworks(t *testing.T) {
	ips, err := ValidateCIDR("192.168.1.0/24")
	if err != nil {
		t.Fatalf("a /24 was refused: %v", err)
	}
	if len(ips) != 254 || ips[0].String() != "192.168.1.1" || ips[253].String() != "192.168.1.254" {
		t.Errorf("a /24 produced %d addresses from %s to %s", len(ips), ips[0], ips[len(ips)-1])
	}

	// A host address with a /24 still means the network it is in, not the tail
	// that starts at that host.
	ips, err = ValidateCIDR("192.168.1.5/24")
	if err != nil || len(ips) != 254 || ips[0].String() != "192.168.1.1" {
		t.Errorf("192.168.1.5/24 produced %d addresses starting at %v (%v)", len(ips), ips[0], err)
	}

	if ips, err := ValidateCIDR("192.168.1.5"); err != nil || len(ips) != 1 || ips[0].String() != "192.168.1.5" {
		t.Errorf("a single host produced %v (%v)", ips, err)
	}

	for _, bad := range []string{"", "8.8.8.0/24", "192.168.0.0/16", "fd00::/64", "not-a-net"} {
		if _, err := ValidateCIDR(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestSuggestionsFollowThePortsAndTheDevice(t *testing.T) {
	host := Host{
		IP: "192.168.1.10", Ports: []int{80, 443, 8123},
		SysDescr: "QNAP QTS 5.1", TemplateIDs: []string{"qnap", "home-assistant"},
	}
	found := suggestions(host, model.Options{"community": "homelab", "version": "2c"})
	byKey := map[string]Suggestion{}
	for _, s := range found {
		byKey[s.Key] = s
		if s.Type == model.SensorSNMP && s.Options["community"] != "homelab" {
			t.Errorf("%s did not carry the scan's SNMP community: %v", s.Key, s.Options)
		}
	}
	for _, key := range []string{"ping", "http-80", "http-443", "tls-443", "snmp-uptime", "qnap.cpu_temp"} {
		if _, ok := byKey[key]; !ok {
			t.Errorf("no %q suggestion; have %v", key, byKey)
		}
	}
	if got := byKey["http-443"]; got.Target != "https://192.168.1.10:443/" {
		t.Errorf("the HTTPS target is %q", got.Target)
	}
	if got := byKey["qnap.cpu_temp"]; !strings.HasPrefix(got.Target, "192.168.1.10") {
		t.Errorf("the template check target is %q", got.Target)
	}
}

func TestScanPortsFindsAListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	found := scanPorts(ctx, net.ParseIP("127.0.0.1"), []int{port, 1})
	if len(found) != 1 || found[0] != port {
		t.Errorf("scanPorts found %v, want [%d]", found, port)
	}
}
