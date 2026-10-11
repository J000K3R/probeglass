package sensor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// ---------------------------------------------------------------------------
// helpers: a certificate whose dates the test controls, and a server for it
// ---------------------------------------------------------------------------

// testCert builds a self-signed certificate. The dates are the point: the whole
// subject here is what happens as a certificate approaches its end, and that is
// not something a test can wait for.
func testCert(t *testing.T, notBefore, notAfter time.Time, names ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: names[0]},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			continue
		}
		tmpl.DNSNames = append(tmpl.DNSNames, n)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// serveTLS starts a TLS listener that completes handshakes and then does
// nothing, which is all this sensor does with the connection.
func serveTLS(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.(*tls.Conn).Handshake()
			}()
		}
	}()
	return l.Addr().String()
}

// sniRecorder keeps the SNI each client offered. It is the only place the name
// the sensor put on the wire can be seen from: everything else about a handshake
// looks the same whichever name was asked for.
type sniRecorder struct {
	mu    sync.Mutex
	names []string
}

func (r *sniRecorder) add(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names = append(r.names, name)
}

// last returns the most recent SNI offered, or "" if none arrived.
func (r *sniRecorder) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.names) == 0 {
		return ""
	}
	return r.names[len(r.names)-1]
}

// serveTLSRecordingSNI is serveTLS with a memory: it answers with the same
// certificate and writes down the name each client asked for.
func serveTLSRecordingSNI(t *testing.T, cert tls.Certificate) (string, *sniRecorder) {
	t.Helper()
	rec := &sniRecorder{}
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		// Returning a nil config keeps the one above, certificate and all; the
		// hook is here to see the hello, not to change the answer.
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			rec.add(hello.ServerName)
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.(*tls.Conn).Handshake()
			}()
		}
	}()
	return l.Addr().String(), rec
}

// closedPort returns an address nothing is listening on. A closed port on
// loopback refuses immediately instead of waiting for a timeout.
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

func TestTLSValidate(t *testing.T) {
	ok := func(target string, opts model.Options) model.Sensor {
		return validSensor(model.Sensor{Name: "cert", Type: model.SensorTLS, Target: target, Options: opts})
	}
	accepted := []model.Sensor{
		// A bare host is what somebody types, and 443 is what they mean.
		ok("example.org", nil),
		ok("example.org:8443", nil),
		ok("nas.local:443", model.Options{"cert_warn_days": 7}),
		ok("example.org", model.Options{"server_name": "www.example.org"}),
		ok("example.org", model.Options{"insecure_skip_verify": true}),
	}
	for _, c := range accepted {
		if err := Validate(c); err != nil {
			t.Errorf("%q with %v was rejected: %v", c.Target, c.Options, err)
		}
	}

	rejected := []struct {
		name   string
		sensor model.Sensor
		want   string
	}{
		{"no target", ok("", nil), "target is required"},
		{"port zero", ok("nas.local:0", nil), "port"},
		{"port too large", ok("nas.local:70000", nil), "port"},
		{"port not a number", ok("nas.local:https", nil), "port"},
		{"no host", ok(":443", nil), "host"},
		{"a whole URL", ok("https://example.org", nil), "without a scheme"},
		{"negative warning window", ok("example.org", model.Options{"cert_warn_days": -1}), "cert_warn_days"},
		{"server_name with a port", ok("example.org", model.Options{"server_name": "example.org:443"}), "bare hostname"},
	}
	for _, tc := range rejected {
		err := Validate(tc.sensor)
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
}

// TestTLSTargetDefaultsThePort pins the one convenience in the target parsing:
// a bare host means 443, and everything else has to be explicit.
func TestTLSTargetDefaultsThePort(t *testing.T) {
	host, addr, err := tlsTarget("example.org")
	if err != nil {
		t.Fatalf("bare host rejected: %v", err)
	}
	if host != "example.org" || addr != "example.org:443" {
		t.Errorf("bare host resolved to host %q addr %q, want example.org / example.org:443", host, addr)
	}

	host, addr, err = tlsTarget("  nas.local:8443  ")
	if err != nil {
		t.Fatalf("host:port rejected: %v", err)
	}
	if host != "nas.local" || addr != "nas.local:8443" {
		t.Errorf("host:port resolved to host %q addr %q", host, addr)
	}
}

// ---------------------------------------------------------------------------
// the probe itself
// ---------------------------------------------------------------------------

// TestTLSRunReportsTheRemainingDays is the ordinary case: a certificate with
// months left is a success, and the days are in the message because that is what
// somebody adds this sensor to see.
//
// Verification is skipped because the certificate is one this test just made.
// The "not verified" note is part of what is asserted: a skipped check must
// never look like a passed one.
func TestTLSRunReportsTheRemainingDays(t *testing.T) {
	addr := serveTLS(t, testCert(t, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour+time.Hour), "127.0.0.1"))

	c := validSensor(model.Sensor{
		Name: "cert", Type: model.SensorTLS, Target: addr,
		Options: model.Options{"insecure_skip_verify": true},
	})
	out := Test(context.Background(), c)
	if !out.Success {
		t.Fatalf("a certificate with 60 days left failed: %s", out.Message)
	}
	if !strings.Contains(out.Message, "certificate valid for 60 day(s)") {
		t.Errorf("message %q does not report the remaining days", out.Message)
	}
	if !strings.Contains(out.Message, "not verified") {
		t.Errorf("message %q does not say that the certificate was not verified", out.Message)
	}
	if out.Latency <= 0 {
		t.Error("a successful probe reported no latency")
	}
}

// TestTLSRunFailsInsideTheWarningWindow is the reason the sensor exists: a
// certificate that still works but is about to stop doing so.
func TestTLSRunFailsInsideTheWarningWindow(t *testing.T) {
	addr := serveTLS(t, testCert(t, time.Now().Add(-time.Hour), time.Now().Add(5*24*time.Hour+time.Hour), "127.0.0.1"))

	c := validSensor(model.Sensor{
		Name: "cert", Type: model.SensorTLS, Target: addr,
		Options: model.Options{"insecure_skip_verify": true},
	})
	out := Test(context.Background(), c)
	if out.Success {
		t.Fatalf("a certificate with 5 days left passed with the default 14-day window: %s", out.Message)
	}
	if !strings.Contains(out.Message, "expires in 5 day(s)") {
		t.Errorf("message %q does not report the remaining days", out.Message)
	}

	// The same certificate passes once the window is turned down. Failing here
	// is the threshold's doing, and the threshold is the user's to set.
	c.Options["cert_warn_days"] = 3
	if out := Test(context.Background(), c); !out.Success {
		t.Errorf("a certificate with 5 days left failed a 3-day window: %s", out.Message)
	}
}

// TestTLSRunFailsOnAnExpiredCertificate covers the case verification cannot
// reach: with the check skipped, an expired certificate completes the handshake,
// and the sensor still has to notice. A zero warning window must not silence it.
func TestTLSRunFailsOnAnExpiredCertificate(t *testing.T) {
	expired := time.Now().Add(-48 * time.Hour)
	addr := serveTLS(t, testCert(t, expired.Add(-365*24*time.Hour), expired, "127.0.0.1"))

	c := validSensor(model.Sensor{
		Name: "cert", Type: model.SensorTLS, Target: addr,
		Options: model.Options{"insecure_skip_verify": true, "cert_warn_days": 0},
	})
	out := Test(context.Background(), c)
	if out.Success {
		t.Fatalf("an expired certificate passed with the warning window switched off: %s", out.Message)
	}
	if !strings.Contains(out.Message, "expired on "+expired.UTC().Format(time.DateOnly)) {
		t.Errorf("message %q does not name the day the certificate ran out", out.Message)
	}
	if out.Err == nil {
		t.Error("a failed outcome should carry an error")
	}
}

// TestTLSRunOnASelfSignedCertificateSaysSo is the everyday homelab case, and
// the one the wording was sharpened for: "not trusted" was three problems
// stacked on top of each other.
func TestTLSRunOnASelfSignedCertificateSaysSo(t *testing.T) {
	addr := serveTLS(t, testCert(t, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour+time.Hour), "127.0.0.1"))

	c := validSensor(model.Sensor{Name: "cert", Type: model.SensorTLS, Target: addr})
	out := Test(context.Background(), c)
	if out.Success {
		t.Fatal("a self-signed certificate passed verification")
	}
	if !strings.Contains(out.Message, "does not trust") {
		t.Errorf("message %q does not name the trust problem", out.Message)
	}
}

// TestTLSRunOnAClosedPort fails on the dial, and says which failure it was.
func TestTLSRunOnAClosedPort(t *testing.T) {
	c := validSensor(model.Sensor{Name: "cert", Type: model.SensorTLS, Target: closedPort(t)})
	out := Test(context.Background(), c)
	if out.Success {
		t.Fatal("probing a closed port succeeded")
	}
	if !strings.Contains(out.Message, "connection refused or unreachable") {
		t.Errorf("message %q does not explain the failure", out.Message)
	}
}

// TestTLSRunOnSomethingThatIsNotTLS covers the service that answers but does not
// speak TLS — a plain HTTP port, say, which is one of the mistakes this sensor
// will meet.
func TestTLSRunOnSomethingThatIsNotTLS(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
				time.Sleep(200 * time.Millisecond)
			}()
		}
	}()

	c := validSensor(model.Sensor{Name: "cert", Type: model.SensorTLS, Target: l.Addr().String()})
	out := Test(context.Background(), c)
	if out.Success {
		t.Fatal("a plain-text service passed as a TLS endpoint")
	}
	if !strings.Contains(out.Message, "TLS handshake failed") {
		t.Errorf("message %q does not say the handshake failed", out.Message)
	}
}

// ---------------------------------------------------------------------------
// what the server_name option does to the handshake
//
// A manual check against a real website cannot answer this: the certificate a
// host happens to present, and whether the name in it matches the one asked for,
// are facts about that host. These tests make both sides, so the name on the wire
// is the only variable.
//
// What they pin is the wiring, not Go's verdict: the name reaches the handshake
// as the SNI, and the same field is the one crypto/tls verifies the certificate
// against. A name *mismatch* cannot be made hermetic end to end, because a
// certificate this test signs itself is never trusted, and a handshake that
// fails on trust never gets as far as the name. The sentence for a mismatch is
// covered by TestDescribeCertErrorTellsTheThreeApart.
// ---------------------------------------------------------------------------

// TestTLSRunSendsTheServerNameAsSNI is the option's first half: the name reaches
// the wire. Verification is skipped here on purpose, so that the handshake
// completes and the SNI can be read back whatever the certificate says.
func TestTLSRunSendsTheServerNameAsSNI(t *testing.T) {
	cert := testCert(t, time.Now().Add(-time.Hour), time.Now().Add(365*24*time.Hour), "watched.example.net")
	addr, sni := serveTLSRecordingSNI(t, cert)

	c := validSensor(model.Sensor{Name: "cert", Type: model.SensorTLS, Target: addr,
		Options: model.Options{"server_name": "watched.example.net", "insecure_skip_verify": true}})
	out := Test(context.Background(), c)
	if !out.Success {
		t.Fatalf("probe failed: %s", out.Message)
	}
	if got := sni.last(); got != "watched.example.net" {
		t.Errorf("the server saw SNI %q, want %q — server_name did not reach the handshake", got, "watched.example.net")
	}
}

// TestTLSRunDefaultsTheServerNameToTheHost covers the option being absent: the
// name to send and to check is then the host in the target. The listener answers
// on a numeric address, so a name arriving at all can only have come from the
// target — the address puts nothing on the wire.
func TestTLSRunDefaultsTheServerNameToTheHost(t *testing.T) {
	cert := testCert(t, time.Now().Add(-time.Hour), time.Now().Add(365*24*time.Hour), "localhost")
	addr, sni := serveTLSRecordingSNI(t, cert)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}

	// Named rather than numeric, so the target has a host for the default to use.
	c := validSensor(model.Sensor{Name: "cert", Type: model.SensorTLS,
		Target:  net.JoinHostPort("localhost", port),
		Options: model.Options{"insecure_skip_verify": true}})
	out := Test(context.Background(), c)
	if !out.Success {
		t.Fatalf("probe failed: %s", out.Message)
	}
	if got := sni.last(); got != "localhost" {
		t.Errorf("the server saw SNI %q, want %q — the target's host was not used", got, "localhost")
	}
}

// ---------------------------------------------------------------------------
// the certificate arithmetic, on its own
// ---------------------------------------------------------------------------

// TestCertInfoDaysRoundDownwards pins the rounding. It is the difference between
// "expired an hour ago" and "expires today", and a truncation towards zero would
// report the second for the first.
//
// The inputs are deliberately not on a day boundary: every one of them is an
// hour past the whole number, so the expected value is not a coin toss.
func TestCertInfoDaysRoundDownwards(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name     string
		notAfter time.Time
		want     int
	}{
		{"sixty days and an hour", now.Add(60*24*time.Hour + time.Hour), 60},
		{"two days and an hour", now.Add(49 * time.Hour), 2},
		{"an hour short of a day", now.Add(23 * time.Hour), 0},
		{"an hour ago", now.Add(-time.Hour), -1},
		{"a day and an hour ago", now.Add(-25 * time.Hour), -2},
	}
	for _, tc := range cases {
		if got := daysUntil(tc.notAfter); got != tc.want {
			t.Errorf("%s: %d days, want %d", tc.name, got, tc.want)
		}
	}
}

// TestCertInfoDescriptions pins the sentences, because they are what an operator
// acts on and they are shared by three sensor types.
func TestCertInfoDescriptions(t *testing.T) {
	runOut := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		ci          certInfo
		warn        int
		wantWarn    bool
		wantMessage string
	}{
		{"comfortable", certInfo{NotAfter: time.Now().Add(90 * 24 * time.Hour), Days: 90}, 14, false, "TLS certificate expires in 90 day(s)"},
		{"inside the window", certInfo{NotAfter: time.Now().Add(3 * 24 * time.Hour), Days: 3}, 14, true, "TLS certificate expires in 3 day(s)"},
		{"on the boundary", certInfo{NotAfter: time.Now().Add(14 * 24 * time.Hour), Days: 14}, 14, true, "TLS certificate expires in 14 day(s)"},
		{"window switched off", certInfo{NotAfter: time.Now().Add(3 * 24 * time.Hour), Days: 3}, 0, false, "TLS certificate expires in 3 day(s)"},
		{"expired, window off", certInfo{NotAfter: runOut, Days: -1}, 0, true, "TLS certificate expired on 2026-09-01"},
		{"expired, window on", certInfo{NotAfter: runOut, Days: -40}, 14, true, "TLS certificate expired on 2026-09-01"},
	}
	for _, tc := range cases {
		if got := tc.ci.warning(tc.warn); got != tc.wantWarn {
			t.Errorf("%s: warning(%d) = %v, want %v", tc.name, tc.warn, got, tc.wantWarn)
		}
		if got := tc.ci.describe(); got != tc.wantMessage {
			t.Errorf("%s: describe() = %q, want %q", tc.name, got, tc.wantMessage)
		}
	}

	if got := (certInfo{NotAfter: runOut, Days: 0}).describeValid(); got != "certificate expires today" {
		t.Errorf("the last day is described as %q", got)
	}
	if got := (certInfo{Days: 82}).describeValid(); got != "certificate valid for 82 day(s)" {
		t.Errorf("a comfortable certificate is described as %q", got)
	}
	if got := (certInfo{Days: 82}).note(); got != " · cert 82d" {
		t.Errorf("note() = %q", got)
	}
}

// TestDescribeCertErrorTellsTheThreeApart is the sharpening, tested where it is
// decided rather than through a handshake: these errors are the ones crypto/tls
// and crypto/x509 actually produce, constructed here by hand.
func TestDescribeCertErrorTellsTheThreeApart(t *testing.T) {
	expired := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			"wrong name",
			// tls wraps verification failures, and Unwrap means errors.As finds
			// the x509 error inside it.
			&tls.CertificateVerificationError{Err: x509.HostnameError{
				Certificate: &x509.Certificate{DNSNames: []string{"example.com"}},
				Host:        "example.org",
			}},
			"example.com",
		},
		{
			"expired",
			x509.CertificateInvalidError{Reason: x509.Expired, Cert: &x509.Certificate{NotAfter: expired}},
			"expired on 2026-09-01",
		},
		{
			"not yet valid",
			x509.CertificateInvalidError{
				Reason: x509.Expired,
				Cert:   &x509.Certificate{NotBefore: time.Now().Add(48 * time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour)},
			},
			"not valid until",
		},
		{
			"unknown authority",
			x509.UnknownAuthorityError{Cert: &x509.Certificate{}},
			"does not trust",
		},
	}
	for _, tc := range cases {
		got, ok := describeCertError(tc.err)
		if !ok {
			t.Errorf("%s: not recognised as a certificate problem", tc.name)
			continue
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q does not mention %q", tc.name, got, tc.want)
		}
	}

	if _, ok := describeCertError(errors.New("connection reset by peer")); ok {
		t.Error("an ordinary network error was reported as a certificate problem")
	}

	// The three sentences must be distinguishable from one another: the whole
	// point of splitting them is that they have three different remedies.
	seen := map[string]string{}
	for _, tc := range cases {
		if msg, ok := describeCertError(tc.err); ok {
			if other, dup := seen[msg]; dup {
				t.Errorf("%s and %s get the same message %q", tc.name, other, msg)
			}
			seen[msg] = tc.name
		}
	}
}

// TestCertFailureWordingIsShared guards the shared wording: an operator who
// learns what a certificate failure looks like on one sensor type must meet the
// same sentence on the others.
func TestCertFailureWordingIsShared(t *testing.T) {
	mine := fmt.Sprintf("%v", expiredSentence(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)))
	viaError, ok := describeCertError(x509.CertificateInvalidError{
		Reason: x509.Expired,
		Cert:   &x509.Certificate{NotAfter: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
	})
	if !ok {
		t.Fatal("an expired certificate error was not recognised")
	}
	if viaError != mine {
		t.Errorf("the two routes to the same failure disagree: %q and %q", mine, viaError)
	}
}
