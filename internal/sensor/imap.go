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

// IMAPProber reads the untagged greeting an IMAP server sends on connect. RFC
// 3501 allows two: `* OK` when the server is ready for a login, and `* PREAUTH`
// when the connection is already authenticated by the transport. Anything else
// — `* BYE` above all — means the mailbox is not available.
//
// Options:
//
//	tls                  connect over implicit TLS (port 993)
//	server_name          SNI/verification name when tls is enabled
//	insecure_skip_verify accept self-signed certificates when tls is enabled
//	expect               substring the greeting must contain
type IMAPProber struct{}

// Type implements Prober.
func (IMAPProber) Type() model.SensorType { return model.SensorIMAP }

// Validate implements Prober.
func (IMAPProber) Validate(c model.Sensor) error {
	if _, _, err := hostPortDefault(c.Target, "143"); err != nil {
		return err
	}
	if len(c.Options.Str("expect", "")) > 200 {
		return errors.New("expect must be at most 200 characters")
	}
	return nil
}

// Run implements Prober.
func (IMAPProber) Run(ctx context.Context, c model.Sensor) Outcome {
	_, addr, err := hostPortDefault(c.Target, "143")
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
		return Errf(time.Since(start), "no IMAP greeting: %s", describeRequestError(err))
	}
	upper := strings.ToUpper(line)
	if !strings.HasPrefix(upper, "* OK") && !strings.HasPrefix(upper, "* PREAUTH") {
		return Outcome{
			Success: false,
			Latency: time.Since(start),
			Message: fmt.Sprintf("the IMAP greeting was %q, expected * OK", truncate(line, 100)),
			Err:     errors.New("unexpected IMAP greeting"),
		}
	}
	if expect := c.Options.Str("expect", ""); expect != "" &&
		!strings.Contains(line, expect) {
		return Outcome{
			Success: false,
			Latency: time.Since(start),
			Message: fmt.Sprintf("IMAP greeting %q does not contain %q", truncate(line, 100), expect),
			Err:     errors.New("unexpected IMAP greeting"),
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
