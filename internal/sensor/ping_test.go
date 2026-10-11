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

// Silence, and the two ways of not getting an answer, are three different
// findings and are reported as three different things: a host that did not
// answer, a packet that never left, and a probe that was stopped. Reading one as
// another sends somebody to look at a firewall that was never the problem, or
// leaves them staring at a target that was never asked.
//
// This is the test that used to be a probe against 198.51.100.7 — a reserved,
// unrouted address that is supposed to stay silent. That was a fine way to reach
// this code, and on a network that behaves it still is; but it makes the result
// depend on the network instead of on this file, and a network that answers for a
// reserved address turns it red with a message about a middlebox, which is what
// happens inside a container here. The judgement is tested directly now, and every
// branch of it, which the network never could.
func TestPingSilenceIsNotASendFailure(t *testing.T) {
	const addr = "198.51.100.7"

	for _, tc := range []struct {
		name       string
		count      int
		sent, loss int
		rtts       []float64
		lastErr    string
		ctxErr     error
		want       []string
		notWant    []string
		success    bool
	}{
		{
			name:  "a host that did not answer",
			count: 1, sent: 1, loss: 1,
			lastErr: "no reply",
			want:    []string{"no ICMP reply from " + addr, "1/1 lost"},
			notWant: []string{"could not send"},
		},
		{
			name:  "a packet that never left",
			count: 1, sent: 0, loss: 1,
			lastErr: "network is unreachable",
			want:    []string{"could not send ICMP to " + addr, "network is unreachable"},
			notWant: []string{"no ICMP reply"},
		},
		{
			name:  "a host refused by the kernel rather than the network",
			count: 1, sent: 0, loss: 1,
			lastErr: "operation not permitted",
			want:    []string{"could not send ICMP to " + addr, icmpHint},
		},
		{
			// A probe stopped before it could send is not evidence either, and it
			// would be misleading to name the send error that was never reached.
			name:  "a probe stopped before it could send",
			count: 1, sent: 0, loss: 1,
			ctxErr:  context.Canceled,
			want:    []string{"was stopped before it could send"},
			notWant: []string{"could not send ICMP to"},
		},
		{
			name:  "an answer",
			count: 3, sent: 3, loss: 0,
			rtts:    []float64{1, 2, 3},
			success: true,
			want:    []string{"ping " + addr, "3/3 replies"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := pingOutcome(addr, tc.count, tc.sent, tc.loss, tc.rtts,
				time.Second, tc.lastErr, tc.ctxErr)
			if out.Success != tc.success {
				t.Errorf("Success = %v, want %v (%q)", out.Success, tc.success, out.Message)
			}
			for _, want := range tc.want {
				if !strings.Contains(out.Message, want) {
					t.Errorf("message %q does not contain %q", out.Message, want)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(out.Message, notWant) {
					t.Errorf("message %q contains %q, which is the other finding",
						out.Message, notWant)
				}
			}
		})
	}
}
