package sensor

import (
	"context"
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

// IANA protocol numbers used by ICMP parsing.
const (
	protoICMPv4 = 1
	protoICMPv6 = 58
)

// PingProber sends ICMP echo requests and measures the replies.
//
// Two socket flavours are tried, in order:
//
//  1. A "datagram" ICMP socket, which on Linux and macOS works without
//     CAP_NET_RAW as long as net.ipv4.ping_group_range permits the group.
//  2. A raw ICMP socket, which needs CAP_NET_RAW. In a container that means
//     cap_add: [NET_RAW] in docker-compose.
//
// If neither works the sensor reports exactly that, because a silent failure
// here is the single most confusing thing about containerised monitoring.
//
// Options:
//
//	count   number of echo requests to send, default 3, max 20
type PingProber struct{}

// Type implements Prober.
func (PingProber) Type() model.SensorType { return model.SensorPing }

// Validate implements Prober.
func (PingProber) Validate(c model.Sensor) error {
	t := strings.TrimSpace(c.Target)
	if t == "" {
		return errors.New("host is required")
	}
	if strings.ContainsAny(t, "/ \t") {
		return errors.New("target must be a hostname or IP address, without a path or port")
	}
	if n := c.Options.Int("count", 3); n < 1 || n > 20 {
		return fmt.Errorf("count must be between 1 and 20, got %d", n)
	}
	return nil
}

// icmpHint is the advice attached to every ICMP socket failure. It names the
// single most common cause, and the one that is hardest to guess from the
// syscall error alone.
const icmpHint = "grant the container CAP_NET_RAW (docker: cap_add: [NET_RAW])"

// openICMP returns a socket for sending echo requests, the IANA protocol number
// that goes with it, and which of the two flavours came back.
//
// Two flavours are tried, in order:
//
//  1. A "datagram" ICMP socket, which on Linux and macOS works without
//     CAP_NET_RAW as long as net.ipv4.ping_group_range permits the group. It
//     receives only the replies to its own requests, which is all an echo probe
//     needs.
//  2. A raw ICMP socket, which needs CAP_NET_RAW.
//
// Which one came back is not trivia: the destination of a write has a different
// type on each flavour, and getting that wrong is a silent, total failure. See
// echoDestination.
//
// Shared with ICMPAvailable so that a diagnostic and a probe can never disagree
// about whether this host can ping at all.
func openICMP(v6 bool) (*icmp.PacketConn, int, bool, error) {
	network, listen, dgram, proto := "ip4:icmp", "0.0.0.0", "udp4", protoICMPv4
	if v6 {
		network, listen, dgram, proto = "ip6:icmp", "::", "udp6", protoICMPv6
	}
	conn, err := icmp.ListenPacket(dgram, "")
	if err == nil {
		return conn, proto, true, nil
	}
	conn, err = icmp.ListenPacket(network, listen)
	if err != nil {
		return nil, proto, false, err
	}
	return conn, proto, false, nil
}

// echoDestination is the address an echo request is written to.
//
// It is a whole function for one sentence in the documentation of
// icmp.PacketConn.WriteTo: the destination "must be net.UDPAddr when c is a
// non-privileged datagram-oriented ICMP endpoint. Otherwise it must be
// net.IPAddr." The datagram endpoint is a net.UDPConn underneath — on macOS and
// Linux it is an ordinary SOCK_DGRAM socket opened with IPPROTO_ICMP — so
// handing it a *net.IPAddr reaches net.UDPConn.WriteTo, which rejects anything
// that is not a *net.UDPAddr with EINVAL before a single packet leaves. Every
// probe then fails within microseconds with "invalid argument", the socket is
// fine, the network is fine, and the dashboard says "no ICMP reply".
//
// The fallback raw endpoint wants the opposite, so the flavour decides. Zone is
// carried over because a link-local IPv6 address without its scope is a
// different address.
func echoDestination(addr *net.IPAddr, dgram bool) net.Addr {
	if !dgram {
		return addr
	}
	return &net.UDPAddr{IP: addr.IP, Zone: addr.Zone}
}

// ICMPAvailable reports whether this process can open an ICMP socket at all.
//
// It is a capability probe, not a reachability test: it opens a socket and
// closes it again without sending anything. That is exactly what a ping sensor
// needs to be able to work, and the usual reason one cannot.
//
// The error carries the driver's own message, because "operation not permitted"
// and "address family not supported" call for different fixes.
func ICMPAvailable() error {
	conn, _, _, err := openICMP(false)
	if err != nil {
		return fmt.Errorf("no ICMP socket available: %s: %w", icmpHint, err)
	}
	conn.Close()
	return nil
}

// Run implements Prober.
func (PingProber) Run(ctx context.Context, c model.Sensor) Outcome {
	host := strings.TrimSpace(c.Target)
	count := c.Options.Int("count", 3)
	if count < 1 {
		count = 1
	}
	if count > 20 {
		count = 20
	}

	start := time.Now()
	ipaddr, err := net.ResolveIPAddr("ip", host)
	if err != nil {
		return Errf(time.Since(start), "resolve %s: %s", host, describeRequestError(err))
	}

	v6 := ipaddr.IP.To4() == nil
	var echoType icmp.Type = ipv4.ICMPTypeEcho
	if v6 {
		echoType = ipv6.ICMPTypeEchoRequest
	}

	conn, proto, dgram, err := openICMP(v6)
	if err != nil {
		return Outcome{
			Success: false,
			Latency: time.Since(start),
			Message: "no ICMP socket available: " + icmpHint,
			Err:     fmt.Errorf("open ICMP socket: %w", err),
		}
	}
	defer conn.Close()
	dst := echoDestination(ipaddr, dgram)

	deadline := time.Now().Add(time.Duration(c.Timeout) * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	perProbe := time.Until(deadline) / time.Duration(count)
	if perProbe < 250*time.Millisecond {
		perProbe = 250 * time.Millisecond
	}

	id := os.Getpid() & 0xffff
	rtts := make([]float64, 0, count)
	loss := 0
	sent := 0
	lastErr := ""
	buf := make([]byte, 1500)

	for seq := 1; seq <= count; seq++ {
		if ctx.Err() != nil {
			break
		}
		probeDeadline := time.Now().Add(perProbe)
		if probeDeadline.After(deadline) {
			probeDeadline = deadline
		}
		_ = conn.SetReadDeadline(probeDeadline)

		msg := icmp.Message{
			Type: echoType,
			Code: 0,
			Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("probeglass")},
		}
		wire, err := msg.Marshal(nil)
		if err != nil {
			lastErr = err.Error()
			loss++
			continue
		}
		sentAt := time.Now()
		if _, err := conn.WriteTo(wire, dst); err != nil {
			lastErr = describeRequestError(err)
			loss++
			continue
		}
		sent++

		matched := false
		for time.Now().Before(probeDeadline) {
			n, peer, err := conn.ReadFrom(buf)
			if err != nil {
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					break
				}
				lastErr = describeRequestError(err)
				break
			}
			parsed, err := icmp.ParseMessage(proto, buf[:n])
			if err != nil {
				continue
			}
			reply, ok := parsed.Body.(*icmp.Echo)
			if !ok || reply.Seq != seq {
				continue
			}
			// The reply arrives addressed as the socket it came in on: a bare
			// address on the raw flavour, the datagram's own peer on the other.
			// Both are checked against the address that was asked for, so a
			// reply from somewhere else is not counted as this probe's.
			switch pa := peer.(type) {
			case *net.IPAddr:
				if !pa.IP.Equal(ipaddr.IP) {
					continue
				}
			case *net.UDPAddr:
				if !pa.IP.Equal(ipaddr.IP) {
					continue
				}
			}
			rtts = append(rtts, float64(time.Since(sentAt).Microseconds())/1000)
			matched = true
			break
		}
		if !matched {
			loss++
			if lastErr == "" {
				lastErr = "no reply"
			}
		}
	}

	return pingOutcome(ipaddr.String(), count, sent, loss, rtts,
		time.Since(start), lastErr, ctx.Err())
}

// pingOutcome turns what the echo loop saw into the result the scheduler stores.
//
// It is split out from Run because it is the part of this prober that carries a
// judgement rather than a measurement — "no reply" and "could not ask" are
// different findings and are fixed differently, and a probe that never got a
// packet out is no evidence about the target at all. A judgement reached through a
// real socket can only be tested on a network that behaves, and whether the network
// behaves is not this file's business: a path that answers for an address reserved
// precisely because nothing may answer for it turns such a test red and names the
// wrong suspect.
//
// ctxErr is passed in rather than read from a context so that the caller decides
// which context it is answering for, and so that this function stays pure.
func pingOutcome(addr string, count, sent, loss int, rtts []float64,
	elapsed time.Duration, lastErr string, ctxErr error) Outcome {
	if len(rtts) == 0 {
		msg := fmt.Sprintf("no ICMP reply from %s (%d/%d lost)", addr, loss, count)
		if sent == 0 {
			msg = fmt.Sprintf("could not send ICMP to %s: %s", addr,
				firstNonEmpty(lastErr, "no request left this host"))
			if ctxErr != nil {
				msg = fmt.Sprintf("ICMP probe to %s was stopped before it could send", addr)
			}
			if looksLikeSocketPermission(lastErr) {
				msg += " · " + icmpHint
			}
		}
		return Outcome{
			Success: false,
			Latency: elapsed,
			Message: msg,
			Err:     errors.New(firstNonEmpty(lastErr, "no reply")),
		}
	}

	avg, minV, maxV := rttStats(rtts)
	return Outcome{
		Success: true,
		Latency: time.Duration(avg * float64(time.Millisecond)),
		Message: fmt.Sprintf("ping %s · avg %.1fms · min %.1fms · max %.1fms · %d/%d replies",
			addr, avg, minV, maxV, count-loss, count),
	}
}

// looksLikeSocketPermission reports whether a send failure reads as the kernel
// refusing the socket rather than the network refusing the packet. It is a
// string test because the flavours come back as different error types — a
// syscall.Errno, a net.OpError wrapping one, or the driver's own wording — and
// the words the kernel uses are the same words on both.
func looksLikeSocketPermission(detail string) bool {
	d := strings.ToLower(detail)
	return strings.Contains(d, "operation not permitted") ||
		strings.Contains(d, "permission denied") ||
		strings.Contains(d, "not permitted")
}

func rttStats(v []float64) (avg, min, max float64) {
	if len(v) == 0 {
		return 0, 0, 0
	}
	min, max = v[0], v[0]
	var sum float64
	for _, x := range v {
		sum += x
		if x < min {
			min = x
		}
		if x > max {
			max = x
		}
	}
	return sum / float64(len(v)), min, max
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
