package sensor

import (
	"encoding/binary"
	"strings"
	"testing"

	"golang.org/x/net/icmp"

	"github.com/j000k3r/probeglass/internal/model"
)

func TestEmbeddedEchoMatchesReadsTheOriginalDatagram(t *testing.T) {
	const id, seq = 4242, 7

	// IPv4: a twenty-byte header and the first eight bytes of the original
	// ICMP message, which is what RFC 792 asks Time Exceeded to carry.
	v4 := make([]byte, 28)
	v4[0] = 0x45 // version 4, header length 5 words
	binary.BigEndian.PutUint16(v4[24:26], id)
	binary.BigEndian.PutUint16(v4[26:28], seq)
	if !embeddedEchoMatches(v4, false, id, seq) {
		t.Error("a matching IPv4 original datagram was not recognised")
	}
	if embeddedEchoMatches(v4, false, id, seq+1) {
		t.Error("a different sequence number was accepted")
	}

	// IPv6: the fixed forty-byte header, then the ICMPv6 header.
	v6 := make([]byte, 48)
	binary.BigEndian.PutUint16(v6[44:46], id)
	binary.BigEndian.PutUint16(v6[46:48], seq)
	if !embeddedEchoMatches(v6, true, id, seq) {
		t.Error("a matching IPv6 original datagram was not recognised")
	}
	if embeddedEchoMatches(v6[:20], true, id, seq) {
		t.Error("a truncated original datagram was accepted")
	}
}

// The hop probe needs a raw ICMP socket, which a CI runner and a laptop user
// usually do not have. When one is available, loopback is a destination that
// always answers with an echo reply.
func TestHopProberReachesLoopback(t *testing.T) {
	probe, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		t.Skipf("no raw ICMP socket available: %v", err)
	}
	_ = probe.Close()

	got := HopProber{}.Run(greetingContext(t), validSensor(model.Sensor{
		Name: "hop", Type: model.SensorHop, Target: "127.0.0.1",
		Options: model.Options{"ttl": 1},
	}))
	if !got.Success {
		t.Fatalf("loopback did not answer: %s", got.Message)
	}
	if !strings.Contains(got.Message, "reached") {
		t.Errorf("message %q does not say the destination answered", got.Message)
	}
}
