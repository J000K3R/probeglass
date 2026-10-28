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

	"github.com/j000k3r/probeglass/internal/model"
)

// TLSProber watches the certificate a service presents, and nothing else.
//
// HTTP and TCP sensors can both read a certificate already, so this type adds no
// capability that was missing. What it adds is a place to say it: a target
// without a protocol in front of it, three options that are all about the
// certificate, and a failure that says which of the three certificate problems
// happened rather than lumping them together.
//
// Options:
//
//	server_name          name to send as SNI and verify against; defaults to the
//	                     host in the target
//	insecure_skip_verify accept any certificate, including expired and self-signed
//	cert_warn_days       fail when the certificate expires within N days,
//	                     default 14
type TLSProber struct{}

// defaultCertWarnDays is the warning window this sensor starts with.
//
// It is 14 rather than the 0 the http and tcp sensors use, because here the
// certificate is the whole subject: somebody who adds a certificate sensor wants
// to hear about a renewal before it is due, not after. Fourteen days is about
// the shortest window that survives a holiday.
const defaultCertWarnDays = 14

// Type implements Prober.
func (TLSProber) Type() model.SensorType { return model.SensorTLS }

// Validate implements Prober.
func (TLSProber) Validate(c model.Sensor) error {
	if _, _, err := tlsTarget(c.Target); err != nil {
		return err
	}
	if c.Options.Int("cert_warn_days", defaultCertWarnDays) < 0 {
		return errors.New("cert_warn_days must not be negative")
	}
	if name := strings.TrimSpace(c.Options.Str("server_name", "")); name != "" {
		// A name with a scheme or a port in it is a target pasted into the wrong
		// field, and saying so here beats a handshake failure later.
		if strings.ContainsAny(name, "/:") {
			return fmt.Errorf("server_name must be a bare hostname, got %q", name)
		}
	}
	return nil
}

// tlsTarget splits a target into the host to verify against and the address to
// dial.
//
// A bare host is the common case — "example.org" is what somebody types for a
// certificate check — and gets port 443. Anything else has to be host:port.
func tlsTarget(raw string) (host, addr string, err error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", "", errors.New("target is required, in host or host:port form")
	}
	// A whole URL is what somebody pastes in out of habit, and it is worth
	// naming rather than reporting as a broken port.
	if strings.Contains(target, "://") {
		return "", "", errors.New("target must be a host or host:port, without a scheme")
	}
	if !strings.Contains(target, ":") {
		return target, net.JoinHostPort(target, "443"), nil
	}
	h, port, splitErr := net.SplitHostPort(target)
	if splitErr != nil {
		return "", "", fmt.Errorf("target must be host or host:port "+
			"(for example example.org:443): %w", splitErr)
	}
	h = strings.TrimSpace(h)
	if h == "" {
		return "", "", errors.New("target must include a host")
	}
	n, convErr := strconv.Atoi(port)
	if convErr != nil || n < 1 || n > 65535 {
		return "", "", fmt.Errorf("port must be a number between 1 and 65535, got %q", port)
	}
	return h, net.JoinHostPort(h, port), nil
}

// Run implements Prober.
func (TLSProber) Run(ctx context.Context, c model.Sensor) Outcome {
	o := c.Options
	host, addr, err := tlsTarget(c.Target)
	if err != nil {
		return Errf(0, "%v", err)
	}

	start := time.Now()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return Errf(time.Since(start), "%s", describeRequestError(err))
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	serverName := strings.TrimSpace(o.Str("server_name", ""))
	if serverName == "" {
		serverName = host
	}
	insecure := o.Bool("insecure_skip_verify", false)

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: insecure, //nolint:gosec // only when the sensor asks for it
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return Errf(time.Since(start), "TLS handshake failed: %s", describeRequestError(err))
	}
	latency := time.Since(start)

	state := tlsConn.ConnectionState()
	ci, ok := leafInfo(state)
	if !ok {
		return Errf(latency, "%s presented no certificate", addr)
	}

	// The version is worth naming: TLS 1.0 and 1.1 are still out there, and a
	// certificate that is fine on a connection nobody should be using is worth
	// knowing about.
	note := tls.VersionName(state.Version)
	if insecure {
		// A skipped verification is not a detail: it turns the sensor into a
		// liveness check for the port, and the number on the card would be a
		// fact about a certificate nobody looked at.
		note += " · not verified"
	}

	if ci.warning(CertWarnDays(c)) {
		reason := "tls certificate expires soon"
		if ci.Days < 0 {
			reason = "tls certificate has expired"
		}
		return Outcome{
			Success:     false,
			Latency:     latency,
			Message:     fmt.Sprintf("%s · %s", note, ci.describe()),
			Err:         errors.New(reason),
			Certificate: ci.certificate(),
		}
	}

	return Outcome{
		Success:     true,
		Latency:     latency,
		Message:     fmt.Sprintf("%s · %s", note, ci.describeValid()),
		Certificate: ci.certificate(),
	}
}
