package sensor

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// startUDPEcho answers every datagram with the given reply.
func startUDPEcho(t *testing.T, reply string) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	go func() {
		buf := make([]byte, 2048)
		for {
			n, peer, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if n > 0 {
				_, _ = conn.WriteTo([]byte(reply), peer)
			}
		}
	}()
	return conn.LocalAddr().String()
}

func TestUDPProberWaitsForTheReply(t *testing.T) {
	addr := startUDPEcho(t, "PONG")
	got := UDPProber{}.Run(greetingContext(t), validSensor(model.Sensor{
		Name: "udp", Type: model.SensorUDP, Target: addr,
		Options: model.Options{"payload": "PING", "expect": "PONG"},
	}))

	if !got.Success {
		t.Fatalf("the reply was not accepted: %s", got.Message)
	}
	if !strings.Contains(got.Message, "PONG") {
		t.Errorf("message %q does not carry the reply", got.Message)
	}
}

func TestUDPProberAcceptsASentDatagramWithoutExpect(t *testing.T) {
	// A black hole: the point is that a sensor which only asks "did the packet
	// leave?" succeeds without inventing a reply.
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer conn.Close()

	got := UDPProber{}.Run(greetingContext(t), validSensor(model.Sensor{
		Name: "udp", Type: model.SensorUDP, Target: conn.LocalAddr().String(),
		Options: model.Options{"payload": "hello"},
	}))

	if !got.Success {
		t.Fatalf("sending failed: %s", got.Message)
	}
	if !strings.Contains(got.Message, "5 bytes") {
		t.Errorf("message %q does not say how much was sent", got.Message)
	}
}

func TestUDPProberReportsAMissingReply(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	got := UDPProber{}.Run(ctx, validSensor(model.Sensor{
		Name: "udp", Type: model.SensorUDP, Target: conn.LocalAddr().String(),
		Options: model.Options{"payload": "PING", "expect": "PONG"},
	}))

	if got.Success {
		t.Fatalf("a missing reply passed: %s", got.Message)
	}
	if !strings.Contains(got.Message, "PONG") {
		t.Errorf("message %q does not name what was awaited", got.Message)
	}
}
