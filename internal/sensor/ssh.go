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

// SSHProber reads the identification line an SSH server sends before any
// handshake: SSH-2.0-OpenSSH_9.6p1, say. That line is the cheapest thing a
// server tells a stranger, and it is enough to tell "sshd is answering" from
// "something is listening on port 22".
//
// Options:
//
//	expect   substring the banner must contain, for pinning a version
type SSHProber struct{}

// Type implements Prober.
func (SSHProber) Type() model.SensorType { return model.SensorSSH }

// Validate implements Prober.
func (SSHProber) Validate(c model.Sensor) error {
	if _, _, err := hostPortDefault(c.Target, "22"); err != nil {
		return err
	}
	if len(c.Options.Str("expect", "")) > 200 {
		return errors.New("expect must be at most 200 characters")
	}
	return nil
}

// Run implements Prober.
func (SSHProber) Run(ctx context.Context, c model.Sensor) Outcome {
	_, addr, err := hostPortDefault(c.Target, "22")
	if err != nil {
		return Errf(0, "%v", err)
	}

	start := time.Now()
	conn, _, err := dialText(ctx, addr, false, "", false)
	if err != nil {
		return Errf(time.Since(start), "%s", describeRequestError(err))
	}
	defer conn.Close()

	// RFC 4253 allows the server to send other lines before its identification
	// line, and a few appliances use that. The first eight are plenty; past that
	// it is not an SSH server that is slow to introduce itself.
	reader := bufio.NewReaderSize(conn, 4096)
	banner := ""
	for range 8 {
		line, readErr := readGreetingLine(reader)
		if readErr != nil {
			return Errf(time.Since(start), "no SSH banner: %s", describeRequestError(readErr))
		}
		if strings.HasPrefix(line, "SSH-") {
			banner = line
			break
		}
	}
	if banner == "" {
		return Outcome{
			Success: false,
			Latency: time.Since(start),
			Message: "the service sent no SSH identification line",
			Err:     errors.New("no SSH banner"),
		}
	}

	if expect := c.Options.Str("expect", ""); expect != "" &&
		!strings.Contains(banner, expect) {
		return Outcome{
			Success: false,
			Latency: time.Since(start),
			Message: fmt.Sprintf("SSH banner %q does not contain %q", truncate(banner, 100), expect),
			Err:     errors.New("unexpected SSH banner"),
		}
	}

	ms := float64(time.Since(start).Microseconds()) / 1000
	return Outcome{
		Success: true,
		Latency: time.Since(start),
		Message: fmt.Sprintf("%s · %.0fms", truncate(banner, 120), ms),
	}
}
