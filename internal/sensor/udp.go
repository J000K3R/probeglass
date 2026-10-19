package sensor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// UDPProber sends one datagram to a UDP port and, when it is told what to look
// for, waits for a reply containing it.
//
// UDP has no handshake, so a sent datagram is not by itself evidence that
// anything is listening: without `expect` the probe can only say that the
// kernel accepted the packet. With `expect` it becomes a real check — DNS over
// UDP, a syslog collector answering, a game server's status reply.
//
// Options:
//
//	payload  text to send, with \r and \n escapes; empty sends an empty datagram
//	expect   substring the reply must contain; empty does not wait for one
type UDPProber struct{}

// Type implements Prober.
func (UDPProber) Type() model.SensorType { return model.SensorUDP }

// Validate implements Prober.
func (UDPProber) Validate(c model.Sensor) error {
	target := strings.TrimSpace(c.Target)
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("target must be host:port (for example 1.1.1.1:53): %w", err)
	}
	if strings.TrimSpace(host) == "" {
		return errors.New("target must include a host")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port must be a number between 1 and 65535, got %q", port)
	}
	if len(c.Options.Str("payload", "")) > 4096 {
		return errors.New("payload must be at most 4096 characters")
	}
	if len(c.Options.Str("expect", "")) > 200 {
		return errors.New("expect must be at most 200 characters")
	}
	return nil
}

// Run implements Prober.
func (UDPProber) Run(ctx context.Context, c model.Sensor) Outcome {
	target := strings.TrimSpace(c.Target)
	start := time.Now()

	// A connected socket, so that an ICMP port-unreachable is reported as an
	// error on the next read instead of being silently dropped.
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", target)
	if err != nil {
		return Errf(time.Since(start), "%s", describeRequestError(err))
	}
	defer conn.Close()

	deadline := time.Now().Add(30 * time.Second)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	payload := unescape(c.Options.Str("payload", ""))
	if _, err := conn.Write([]byte(payload)); err != nil {
		return Errf(time.Since(start), "send failed: %s", describeRequestError(err))
	}

	expect := c.Options.Str("expect", "")
	ms := float64(time.Since(start).Microseconds()) / 1000
	if expect == "" {
		return Outcome{
			Success: true,
			Latency: time.Since(start),
			Message: fmt.Sprintf("UDP datagram sent to %s · %d bytes · %.0fms", target, len(payload), ms),
		}
	}

	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			reply := string(buf[:n])
			if strings.Contains(reply, expect) {
				return Outcome{
					Success: true,
					Latency: time.Since(start),
					Message: fmt.Sprintf("UDP reply from %s · %q · %.0fms",
						target, truncate(reply, 80), float64(time.Since(start).Microseconds())/1000),
				}
			}
		}
		if err != nil {
			if osIsTimeout(err) {
				return Outcome{
					Success: false,
					Latency: time.Since(start),
					Message: fmt.Sprintf("no UDP reply containing %q within the timeout", expect),
					Err:     errors.New("timeout waiting for UDP reply"),
				}
			}
			return Errf(time.Since(start), "read failed: %s", describeRequestError(err))
		}
	}
}
