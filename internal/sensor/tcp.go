package sensor

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/j000k3r/weatherglass/internal/model"
)

// TCPProber verifies that a TCP port accepts connections. It optionally
// performs a TLS handshake and/or a small request/response exchange, which is
// enough to watch most non-HTTP services (SMTP, IMAP, MQTT, PostgreSQL, ...).
//
// Options:
//
//	tls                  perform a TLS handshake after connecting
//	server_name          SNI/verification name when tls is enabled
//	insecure_skip_verify accept self-signed certificates when tls is enabled
//	send                 text to write after connecting (supports \r and \n)
//	expect               substring the response must contain
type TCPProber struct{}

// Type implements Prober.
func (TCPProber) Type() model.SensorType { return model.SensorTCP }

// Validate implements Prober.
func (TCPProber) Validate(c model.Sensor) error {
	target := strings.TrimSpace(c.Target)
	if target == "" {
		return errors.New("target is required, in host:port form")
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("target must be host:port (for example nas.local:445): %w", err)
	}
	if strings.TrimSpace(host) == "" {
		return errors.New("target must include a host")
	}
	if strings.Contains(host, "/") {
		return errors.New("target must not contain a path")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port must be a number between 1 and 65535, got %q", port)
	}
	if c.Options.Int("cert_warn_days", 0) < 0 {
		return errors.New("cert_warn_days must not be negative")
	}
	return nil
}

// Run implements Prober.
func (TCPProber) Run(ctx context.Context, c model.Sensor) Outcome {
	o := c.Options
	target := strings.TrimSpace(c.Target)
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return Errf(0, "invalid target %q: %v", target, err)
	}

	start := time.Now()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", target)
	latency := time.Since(start)
	if err != nil {
		return Errf(latency, "%s", describeRequestError(err))
	}
	defer conn.Close()

	deadline := time.Now().Add(30 * time.Second)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	note := ""
	if o.Bool("tls", false) {
		serverName := o.Str("server_name", host)
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: o.Bool("insecure_skip_verify", false), //nolint:gosec
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return Errf(time.Since(start), "TLS handshake failed: %s", describeRequestError(err))
		}
		state := tlsConn.ConnectionState()
		note = fmt.Sprintf(" · %s", tls.VersionName(state.Version))
		if ci, ok := leafInfo(state); ok {
			note += ci.note()
			if ci.warning(o.Int("cert_warn_days", 0)) {
				return Outcome{
					Success: false,
					Latency: time.Since(start),
					Message: ci.describe(),
					Err:     errors.New("tls certificate expires soon"),
				}
			}
		}
		conn = tlsConn
	}

	if send := unescape(o.Str("send", "")); send != "" {
		if _, err := conn.Write([]byte(send)); err != nil {
			return Errf(latency, "write failed: %s", describeRequestError(err))
		}
	}

	expect := unescape(o.Str("expect", ""))
	// The banner is the reason this sensor type is useful beyond a plain port
	// scan, so it is captured whenever a reply is requested or a response
	// arrives promptly.
	if expect != "" {
		buf := make([]byte, 4096)
		var got []byte
		for len(got) < 64*1024 {
			n, err := conn.Read(buf)
			if n > 0 {
				got = append(got, buf[:n]...)
				if strings.Contains(string(got), expect) {
					return Outcome{
						Success: true,
						Latency: time.Since(start),
						Message: fmt.Sprintf("TCP connect %s · replied in %.0fms%s",
							target, float64(time.Since(start).Microseconds())/1000, note),
					}
				}
			}
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) || osIsTimeout(err) {
					return Outcome{
						Success: false,
						Latency: time.Since(start),
						Message: fmt.Sprintf("no response containing %q within the timeout", expect),
						Err:     errors.New("timeout waiting for response"),
					}
				}
				if len(got) > 0 {
					return Outcome{
						Success: false,
						Latency: time.Since(start),
						Message: fmt.Sprintf("response %q does not contain %q", truncate(string(got), 80), expect),
						Err:     errors.New("unexpected response"),
					}
				}
				return Errf(time.Since(start), "read failed: %s", describeRequestError(err))
			}
		}
		return Outcome{
			Success: false,
			Latency: time.Since(start),
			Message: fmt.Sprintf("response too large and does not contain %q", expect),
			Err:     errors.New("unexpected response"),
		}
	}

	ms := float64(time.Since(start).Microseconds()) / 1000
	return Outcome{
		Success: true,
		Latency: time.Since(start),
		Message: fmt.Sprintf("TCP connect %s · %.0fms%s", target, ms, note),
	}
}

// unescape expands the escape sequences that make banner probes readable in a
// form field.
func unescape(s string) string {
	if s == "" {
		return s
	}
	r := strings.NewReplacer(`\r`, "\r", `\n`, "\n", `\t`, "\t", `\0`, "\x00")
	return r.Replace(s)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func osIsTimeout(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return false
}
