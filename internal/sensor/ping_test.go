package sensor

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/weatherglass/internal/model"
)

// The destination of an echo request has a different type on each of the two
// sockets the prober opens, and WriteTo rejects the wrong one outright — with
// EINVAL, before a packet leaves, on the socket that is meant to work without
// special privileges. That failure is invisible from the outside: every probe
// fails in microseconds and the dashboard says "no ICMP reply", so nobody goes
// looking at the socket. This test is the guard on the one line that decides it.
func TestEchoDestinationMatchesTheSocket(t *testing.T) {
	addr := &net.IPAddr{IP: net.ParseIP("192.168.11.1")}

	got := echoDestination(addr, true)
	udp, ok := got.(*net.UDPAddr)
	if !ok {
		t.Fatalf("a datagram ICMP socket wants a *net.UDPAddr, got %T", got)
	}
	if !udp.IP.Equal(addr.IP) {
		t.Errorf("the datagram destination is %s, not %s", udp.IP, addr.IP)
	}

	raw := echoDestination(addr, false)
	if _, ok := raw.(*net.IPAddr); !ok {
		t.Fatalf("a raw ICMP socket wants a *net.IPAddr, got %T", raw)
	}

	// A link-local IPv6 address without its zone is a different address, so the
	// scope has to survive the conversion.
	scoped := echoDestination(&net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "en0"}, true)
	if z := scoped.(*net.UDPAddr).Zone; z != "en0" {
		t.Errorf("the scope was dropped: zone is %q, not %q", z, "en0")
	}
}

// TestPingLoopback runs the whole probe — socket, send, read, parse — against
// the one host that is always there and never needs permission from the network.
//
// It is skipped when this host cannot open an ICMP socket at all, which is a
// real configuration (a container without NET_RAW and without a ping group) and
// one the prober reports on its own terms; asserting on that here would only
// restate the platform.
func TestPingLoopback(t *testing.T) {
	if err := ICMPAvailable(); err != nil {
		t.Skipf("this host has no ICMP socket: %v", err)
	}

	c := validSensor(model.Sensor{Name: "loopback", Type: model.SensorPing,
		Target: "127.0.0.1", Options: model.Options{"count": 2}})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out := PingProber{}.Run(ctx, c)
	if !out.Success {
		t.Fatalf("pinging the loopback address failed: %s (%v)", out.Message, out.Err)
	}
	if !strings.Contains(out.Message, "2/2 replies") {
		t.Errorf("message %q does not report both replies", out.Message)
	}
	if out.Latency <= 0 {
		t.Errorf("latency is %v, which cannot be a round trip", out.Latency)
	}
}

// A host that is not there is silence, and silence is reported as silence —
// the loss count and the address, with no invented cause.
func TestPingUnreachableIsSilence(t *testing.T) {
	if err := ICMPAvailable(); err != nil {
		t.Skipf("this host has no ICMP socket: %v", err)
	}

	// 198.51.100.0/24 is reserved for documentation and is not routed, so the
	// request leaves and nothing answers. Not a name, so no resolver is asked.
	c := validSensor(model.Sensor{Name: "nowhere", Type: model.SensorPing,
		Target: "198.51.100.7", Options: model.Options{"count": 1}})
	c.Timeout = 1

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out := PingProber{}.Run(ctx, c)
	if out.Success {
		t.Fatalf("a reserved, unrouted address answered: %s", out.Message)
	}
	if !strings.Contains(out.Message, "198.51.100.7") {
		t.Errorf("message %q does not name the address", out.Message)
	}
	if strings.Contains(out.Message, "could not send") {
		t.Errorf("the request left the host, so this is silence, not a send failure: %q", out.Message)
	}
}
