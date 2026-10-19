package sensor

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/j000k3r/probeglass/internal/model"
)

// HopProber sends one ICMP echo with a chosen time-to-live and reports what
// answered. A TTL of 1 asks the first router; a larger one asks the router that
// many hops away, or the destination itself when the packet gets that far. It
// is the difference between "the gateway is up" and "the path out of the house
// still exists".
//
// A router answers a packet whose TTL has run out with ICMP Time Exceeded, and
// that answer is the success of this probe: the hop exists and named itself.
//
// Options:
//
//	ttl   time-to-live to send with, 1-255, default 1
type HopProber struct{}

// Type implements Prober.
func (HopProber) Type() model.SensorType { return model.SensorHop }

// Validate implements Prober.
func (HopProber) Validate(c model.Sensor) error {
	t := strings.TrimSpace(c.Target)
	if t == "" {
		return errors.New("host is required")
	}
	if strings.ContainsAny(t, "/ \t") {
		return errors.New("target must be a hostname or IP address, without a path or port")
	}
	if n := c.Options.Int("ttl", 1); n < 1 || n > 255 {
		return fmt.Errorf("ttl must be between 1 and 255, got %d", n)
	}
	return nil
}

// Run implements Prober.
func (HopProber) Run(ctx context.Context, c model.Sensor) Outcome {
	host := strings.TrimSpace(c.Target)
	ttl := c.Options.Int("ttl", 1)
	if ttl < 1 {
		ttl = 1
	}
	if ttl > 255 {
		ttl = 255
	}

	start := time.Now()
	ipaddr, err := net.ResolveIPAddr("ip", host)
	if err != nil {
		return Errf(time.Since(start), "resolve %s: %s", host, describeRequestError(err))
	}
	v6 := ipaddr.IP.To4() == nil

	echoType := icmp.Type(ipv4.ICMPTypeEcho)
	proto := protoICMPv4
	network, listen := "ip4:icmp", "0.0.0.0"
	if v6 {
		echoType = ipv6.ICMPTypeEchoRequest
		proto = protoICMPv6
		network, listen = "ip6:icmp", "::"
	}

	// A raw socket, not the datagram one the ping prober prefers: a ping socket
	// reports an ICMP error as a socket error, while a hop sensor needs the Time
	// Exceeded message itself, because the router's address is in it.
	conn, err := icmp.ListenPacket(network, listen)
	if err != nil {
		return Outcome{
			Success: false,
			Latency: time.Since(start),
			Message: "no raw ICMP socket: " + icmpHint,
			Err:     fmt.Errorf("open raw ICMP socket: %w", err),
		}
	}
	defer conn.Close()

	if v6 {
		p := conn.IPv6PacketConn()
		if p == nil {
			return Errf(time.Since(start), "no IPv6 packet connection for the ICMP socket")
		}
		err = p.SetHopLimit(ttl)
	} else {
		p := conn.IPv4PacketConn()
		if p == nil {
			return Errf(time.Since(start), "no IPv4 packet connection for the ICMP socket")
		}
		err = p.SetTTL(ttl)
	}
	if err != nil {
		return Errf(time.Since(start), "set ttl %d: %v", ttl, err)
	}

	id := os.Getpid() & 0xffff
	const seq = 1
	msg := icmp.Message{
		Type: echoType,
		Code: 0,
		Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("probeglass hop")},
	}
	wire, err := msg.Marshal(nil)
	if err != nil {
		return Errf(time.Since(start), "build echo: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetReadDeadline(deadline)

	dst := &net.IPAddr{IP: ipaddr.IP, Zone: ipaddr.Zone}
	sentAt := time.Now()
	if _, err := conn.WriteTo(wire, dst); err != nil {
		return Errf(time.Since(start), "send echo: %s", describeRequestError(err))
	}

	buf := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			if osIsTimeout(err) || ctx.Err() != nil {
				return Outcome{
					Success: false,
					Latency: time.Since(sentAt),
					Message: fmt.Sprintf("hop %d · no ICMP reply within the timeout", ttl),
					Err:     errors.New("no ICMP reply"),
				}
			}
			return Errf(time.Since(start), "read: %s", describeRequestError(err))
		}

		parsed, err := icmp.ParseMessage(proto, buf[:n])
		if err != nil {
			continue
		}
		from := ""
		if addr, ok := peer.(*net.IPAddr); ok {
			from = addr.IP.String()
		}

		switch body := parsed.Body.(type) {
		case *icmp.Echo:
			if parsed.Type == ipv4.ICMPTypeEchoReply || parsed.Type == ipv6.ICMPTypeEchoReply {
				if body.ID != id || body.Seq != seq {
					continue
				}
				ms := float64(time.Since(sentAt).Microseconds()) / 1000
				return Outcome{
					Success: true,
					Latency: time.Since(sentAt),
					Message: fmt.Sprintf("hop %d · reached %s · %.0fms", ttl, from, ms),
				}
			}
		case *icmp.TimeExceeded:
			if !embeddedEchoMatches(body.Data, v6, id, seq) {
				continue
			}
			ms := float64(time.Since(sentAt).Microseconds()) / 1000
			return Outcome{
				Success: true,
				Latency: time.Since(sentAt),
				Message: fmt.Sprintf("hop %d · %s · time exceeded · %.0fms", ttl, from, ms),
			}
		}
	}
}

// embeddedEchoMatches reads the echo id and sequence out of the original
// datagram a Time Exceeded message carries, so that one sensor cannot claim a
// reply that belongs to another probe running at the same time.
func embeddedEchoMatches(data []byte, v6 bool, id, seq int) bool {
	var offset int
	if v6 {
		// The fixed IPv6 header is forty bytes, and the original ICMPv6 header
		// follows it.
		offset = 40
	} else {
		if len(data) < 20 {
			return false
		}
		offset = int(data[0]&0x0f) * 4
	}
	if offset < 0 || len(data) < offset+8 {
		return false
	}
	gotID := int(binary.BigEndian.Uint16(data[offset+4 : offset+6]))
	gotSeq := int(binary.BigEndian.Uint16(data[offset+6 : offset+8]))
	return gotID == id && gotSeq == seq
}
