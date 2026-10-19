package sensor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// SMTPProber reads the greeting an SMTP server sends on connect. RFC 5321 says
// it begins with 220, and a server that cannot answer with that is not ready to
// take mail — which is worth knowing before the queue backs up.
//
// Options:
//
//	tls                  connect over implicit TLS (port 465, not STARTTLS)
//	server_name          SNI/verification name when tls is enabled
//	insecure_skip_verify accept self-signed certificates when tls is enabled
//	expect               substring the greeting must contain
type SMTPProber struct{}

// Type implements Prober.
func (SMTPProber) Type() model.SensorType { return model.SensorSMTP }

// Validate implements Prober.
func (SMTPProber) Validate(c model.Sensor) error {
	if _, _, err := hostPortDefault(c.Target, "25"); err != nil {
		return err
	}
	if len(c.Options.Str("expect", "")) > 200 {
		return errors.New("expect must be at most 200 characters")
	}
	return nil
}

// Run implements Prober.
func (SMTPProber) Run(ctx context.Context, c model.Sensor) Outcome {
	_, addr, err := hostPortDefault(c.Target, "25")
	if err != nil {
		return Errf(0, "%v", err)
	}

	start := time.Now()
	conn, tlsNote, err := dialText(ctx, addr,
		c.Options.Bool("tls", false),
		c.Options.Str("server_name", ""),
		c.Options.Bool("insecure_skip_verify", false))
	if err != nil {
		return Errf(time.Since(start), "%s", describeRequestError(err))
	}
	defer conn.Close()

	line, err := readGreetingLine(bufio.NewReaderSize(conn, 4096))
	if err != nil {
		return Errf(time.Since(start), "no SMTP greeting: %s", describeRequestError(err))
	}
	if !strings.HasPrefix(line, "220") {
		return Outcome{
			Success: false,
			Latency: time.Since(start),
			Message: fmt.Sprintf("the SMTP greeting was %q, expected 220", truncate(line, 100)),
			Err:     errors.New("unexpected SMTP greeting"),
		}
	}
	if expect := c.Options.Str("expect", ""); expect != "" &&
		!strings.Contains(line, expect) {
		return Outcome{
			Success: false,
			Latency: time.Since(start),
			Message: fmt.Sprintf("SMTP greeting %q does not contain %q", truncate(line, 100), expect),
			Err:     errors.New("unexpected SMTP greeting"),
		}
	}

	note := ""
	if tlsNote != "" {
		note = " · " + tlsNote
	}
	ms := float64(time.Since(start).Microseconds()) / 1000
	return Outcome{
		Success: true,
		Latency: time.Since(start),
		Message: fmt.Sprintf("%s · %.0fms%s", truncate(line, 120), ms, note),
	}
}
