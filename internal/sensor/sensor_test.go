package sensor

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/j000k3r/weatherglass/internal/model"
)

func validSensor(c model.Sensor) model.Sensor {
	c.Interval, c.Timeout, c.Retries = 60, 10, 1
	return c
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

func TestValidateAcceptsEveryType(t *testing.T) {
	cases := map[string]model.Sensor{
		"http": validSensor(model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://example.org"}),
		"tcp":  validSensor(model.Sensor{Name: "ssh", Type: model.SensorTCP, Target: "nas.local:22"}),
		"ping": validSensor(model.Sensor{Name: "gw", Type: model.SensorPing, Target: "192.168.1.1"}),
		"dns": validSensor(model.Sensor{
			Name: "dns", Type: model.SensorDNS, Target: "example.org",
			Options: model.Options{"record_type": "A", "resolver": "1.1.1.1"},
		}),
		"tls": validSensor(model.Sensor{
			Name: "cert", Type: model.SensorTLS, Target: "example.org:443",
			Options: model.Options{"cert_warn_days": 14},
		}),
		"docker": validSensor(model.Sensor{
			Name: "container", Type: model.SensorDocker, Target: "nginx",
			Options: model.Options{"expect_health": "healthy", "max_restarts": 3},
		}),
		"snmp": validSensor(model.Sensor{
			Name: "switch", Type: model.SensorSNMP, Target: "192.168.1.2",
			Options: model.Options{"oid": "1.3.6.1.2.1.1.3.0", "min": 1},
		}),
	}
	for name, c := range cases {
		if err := Validate(c); err != nil {
			t.Errorf("%s: a valid sensor was rejected: %v", name, err)
		}
	}
}

func TestValidateRejectsBadSensors(t *testing.T) {
	cases := []struct {
		name   string
		sensor model.Sensor
		want   string
	}{
		{"no name", validSensor(model.Sensor{Type: model.SensorHTTP, Target: "https://example.org"}), "name"},
		{"unknown type", validSensor(model.Sensor{Name: "x", Type: "carrier-pigeon", Target: "a"}), "unsupported sensor type"},
		{"interval too short", model.Sensor{Name: "x", Type: model.SensorHTTP, Target: "https://example.org", Interval: 1, Timeout: 1, Retries: 1}, "at least 5 seconds"},
		{"timeout above twice the interval", model.Sensor{Name: "x", Type: model.SensorHTTP, Target: "https://example.org", Interval: 10, Timeout: 300, Retries: 1}, "twice the interval"},
		{"no retries", model.Sensor{Name: "x", Type: model.SensorHTTP, Target: "https://example.org", Interval: 60, Timeout: 10, Retries: 0}, "retries"},
		{"http without scheme", validSensor(model.Sensor{Name: "x", Type: model.SensorHTTP, Target: "example.org"}), "scheme"},
		{"http with odd scheme", validSensor(model.Sensor{Name: "x", Type: model.SensorHTTP, Target: "ftp://example.org"}), "scheme"},
		{"http with bad status spec", validSensor(model.Sensor{Name: "x", Type: model.SensorHTTP, Target: "https://example.org", Options: model.Options{"expected_status": "99"}}), "status"},
		{"http with bad regex", validSensor(model.Sensor{Name: "x", Type: model.SensorHTTP, Target: "https://example.org", Options: model.Options{"body_regex": "([unclosed"}}), "regular expression"},
		{"tcp without port", validSensor(model.Sensor{Name: "x", Type: model.SensorTCP, Target: "nas.local"}), "host:port"},
		{"tcp with a bad port", validSensor(model.Sensor{Name: "x", Type: model.SensorTCP, Target: "nas.local:99999"}), "port"},
		{"ping with a path", validSensor(model.Sensor{Name: "x", Type: model.SensorPing, Target: "192.168.1.1/24"}), "without a path"},
		{"ping count out of range", validSensor(model.Sensor{Name: "x", Type: model.SensorPing, Target: "192.168.1.1", Options: model.Options{"count": 99}}), "count"},
		{"dns with an unknown record type", validSensor(model.Sensor{Name: "x", Type: model.SensorDNS, Target: "example.org", Options: model.Options{"record_type": "PTR9"}}), "record_type"},
		{"dns with a bad resolver", validSensor(model.Sensor{Name: "x", Type: model.SensorDNS, Target: "example.org", Options: model.Options{"resolver": "not a host"}}), "resolver"},
	}
	for _, tc := range cases {
		err := Validate(tc.sensor)
		if err == nil {
			t.Errorf("%s: accepted an invalid sensor", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
}

func TestParseStatusSpec(t *testing.T) {
	accept := []struct {
		spec string
		code int
		want bool
	}{
		{"", 200, true},
		{"", 399, true},
		{"", 400, false},
		{"200", 200, true},
		{"200", 201, false},
		{"200-299", 250, true},
		{"204,301", 301, true},
		{"204,301", 300, false},
		{"2xx", 250, true},
		{"5xx", 250, false},
		{" 200 - 299 ", 250, true},
	}
	for _, tc := range accept {
		spec, err := parseStatusSpec(tc.spec)
		if err != nil {
			t.Errorf("parseStatusSpec(%q): %v", tc.spec, err)
			continue
		}
		if got := spec.match(tc.code); got != tc.want {
			t.Errorf("parseStatusSpec(%q).match(%d) = %v, want %v", tc.spec, tc.code, got, tc.want)
		}
	}

	reject := []string{"99", "600", "abc", "2xx9", "300-200", "-", "0xx", "6xx"}
	for _, spec := range reject {
		if _, err := parseStatusSpec(spec); err == nil {
			t.Errorf("parseStatusSpec(%q) accepted an invalid spec", spec)
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

func TestHTTPRunAgainstLocalServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/teapot":
			w.WriteHeader(http.StatusTeapot)
		case "/body":
			w.Header().Set("X-Server-Name", "weatherglass-test")
			_, _ = w.Write([]byte("hello homelab"))
		case "/echo":
			// Echoing the request back keeps the assertion in the outcome
			// instead of in shared state written from the handler goroutine.
			_, _ = fmt.Fprintf(w, "auth=%s ua=%s homelab=%s",
				r.Header.Get("Authorization"), r.Header.Get("User-Agent"), r.Header.Get("X-Homelab"))
		case "/hang":
			<-r.Context().Done()
		default:
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()

	tests := []struct {
		name    string
		target  string
		options model.Options
		timeout int
		wantOK  bool
		want    string
	}{
		{
			name:   "plain success",
			target: srv.URL,
			wantOK: true,
			want:   "HTTP 200",
		},
		{
			// Success means body_contains matched the echoed credentials, which
			// is what proves the token, the custom header and the User-Agent
			// actually went out on the wire.
			name:    "credentials and headers are sent",
			target:  srv.URL + "/echo",
			options: model.Options{"bearer_token": "s3cr3t", "user_agent": "wg-test", "headers": map[string]any{"X-Homelab": "yes"}, "body_contains": "auth=Bearer s3cr3t ua=wg-test homelab=yes"},
			wantOK:  true,
		},
		{
			name:    "the default user agent identifies the daemon",
			target:  srv.URL + "/echo",
			options: model.Options{"body_contains": "ua=Weatherglass/"},
			wantOK:  true,
		},
		{
			name:    "a different token would be caught",
			target:  srv.URL + "/echo",
			options: model.Options{"bearer_token": "wrong", "body_contains": "auth=Bearer s3cr3t"},
			wantOK:  false,
			want:    "does not contain",
		},
		{
			name:   "unexpected status",
			target: srv.URL + "/teapot",
			wantOK: false,
			want:   "HTTP 418, expected 200-399",
		},
		{
			name:    "status spec accepts the teapot",
			target:  srv.URL + "/teapot",
			options: model.Options{"expected_status": "418"},
			wantOK:  true,
		},
		{
			name:    "body_contains hit",
			target:  srv.URL + "/body",
			options: model.Options{"body_contains": "homelab"},
			wantOK:  true,
		},
		{
			name:    "body_contains miss",
			target:  srv.URL + "/body",
			options: model.Options{"body_contains": "kubernetes"},
			wantOK:  false,
			want:    "does not contain",
		},
		{
			name:    "body_not_contains",
			target:  srv.URL + "/body",
			options: model.Options{"body_not_contains": "hello"},
			wantOK:  false,
			want:    "contains",
		},
		{
			name:    "body_regex hit",
			target:  srv.URL + "/body",
			options: model.Options{"body_regex": `hel+o\s+\w+`},
			wantOK:  true,
		},
		{
			name:    "body_regex miss",
			target:  srv.URL + "/body",
			options: model.Options{"body_regex": `^\d+$`},
			wantOK:  false,
			want:    "does not match",
		},
		{
			name:    "expected_headers hit",
			target:  srv.URL + "/body",
			options: model.Options{"expected_headers": map[string]any{"X-Server-Name": "weatherglass"}},
			wantOK:  true,
		},
		{
			name:    "expected_headers miss",
			target:  srv.URL + "/body",
			options: model.Options{"expected_headers": map[string]any{"X-Server-Name": "nginx"}},
			wantOK:  false,
			want:    "expected to contain",
		},
		{
			name:    "method override",
			target:  srv.URL + "/teapot",
			options: model.Options{"method": "HEAD", "expected_status": "418"},
			wantOK:  true,
		},
		{
			name:    "timeout is honoured",
			target:  srv.URL + "/hang",
			timeout: 1,
			wantOK:  false,
			want:    "timeout",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := validSensor(model.Sensor{Name: tc.name, Type: model.SensorHTTP, Target: tc.target, Options: tc.options})
			if tc.timeout > 0 {
				c.Timeout = tc.timeout
			}
			if err := Validate(c); err != nil {
				t.Fatalf("test sensor is not valid: %v", err)
			}
			out := Test(context.Background(), c)
			if out.Success != tc.wantOK {
				t.Fatalf("Success = %v, want %v (message: %s)", out.Success, tc.wantOK, out.Message)
			}
			if tc.want != "" && !strings.Contains(out.Message, tc.want) {
				t.Errorf("message %q does not contain %q", out.Message, tc.want)
			}
			if out.Latency <= 0 {
				t.Errorf("latency = %s, want a measured duration", out.Latency)
			}
		})
	}
}

func TestHTTPFailureIsInformative(t *testing.T) {
	// Nothing listens on this port: a closed port on loopback fails immediately
	// instead of waiting for a timeout.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	c := validSensor(model.Sensor{Name: "closed", Type: model.SensorHTTP, Target: "http://" + addr})
	out := Test(context.Background(), c)
	if out.Success {
		t.Fatalf("probing a closed port succeeded")
	}
	if out.Err == nil {
		t.Error("a failed outcome should carry an error")
	}
	if strings.TrimSpace(out.Message) == "" {
		t.Error("a failed outcome should carry a human-readable message")
	}
}

// ---------------------------------------------------------------------------
// TCP
// ---------------------------------------------------------------------------

func TestTCPRunAgainstLocalListener(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	// A stub banner service: it answers every connection with a fixed greeting,
	// which is what the send/expect options are for.
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 64)
				_, _ = conn.Read(buf)
				_, _ = conn.Write([]byte("220 homelab SMTP ready\r\n"))
			}()
		}
	}()

	port := l.Addr().(*net.TCPAddr).Port
	target := "127.0.0.1:" + strconv.Itoa(port)

	t.Run("plain connect", func(t *testing.T) {
		c := validSensor(model.Sensor{Name: "smtp", Type: model.SensorTCP, Target: target})
		out := Test(context.Background(), c)
		if !out.Success {
			t.Fatalf("connect failed: %s", out.Message)
		}
		if !strings.Contains(out.Message, "TCP connect") {
			t.Errorf("message %q does not describe the connect", out.Message)
		}
	})

	t.Run("banner match", func(t *testing.T) {
		c := validSensor(model.Sensor{Name: "smtp", Type: model.SensorTCP, Target: target,
			Options: model.Options{"send": `EHLO homelab\r\n`, "expect": "SMTP"}})
		out := Test(context.Background(), c)
		if !out.Success {
			t.Fatalf("banner probe failed: %s", out.Message)
		}
		if !strings.Contains(out.Message, "replied in") {
			t.Errorf("message %q does not mention the reply", out.Message)
		}
	})

	t.Run("banner mismatch", func(t *testing.T) {
		c := validSensor(model.Sensor{Name: "smtp", Type: model.SensorTCP, Target: target,
			Options: model.Options{"send": `EHLO homelab\r\n`, "expect": "IMAP"}})
		out := Test(context.Background(), c)
		if out.Success {
			t.Fatal("a wrong banner was accepted")
		}
		if !strings.Contains(out.Message, "does not contain") {
			t.Errorf("message %q does not explain the mismatch", out.Message)
		}
	})

	t.Run("refused connection", func(t *testing.T) {
		closed, _ := net.Listen("tcp", "127.0.0.1:0")
		closedAddr := closed.Addr().String()
		closed.Close()

		c := validSensor(model.Sensor{Name: "smtp", Type: model.SensorTCP, Target: closedAddr})
		out := Test(context.Background(), c)
		if out.Success {
			t.Fatal("connecting to a closed port succeeded")
		}
		if strings.TrimSpace(out.Message) == "" {
			t.Error("the failure has no message")
		}
	})
}

func TestUnescape(t *testing.T) {
	got := unescape(`QUIT\r\n`)
	if got != "QUIT\r\n" {
		t.Errorf("unescape = %q, want a real CRLF", got)
	}
	if unescape("") != "" {
		t.Error("unescape of an empty string should stay empty")
	}
}

// ---------------------------------------------------------------------------
// DNS
// ---------------------------------------------------------------------------

// startDNSStub answers A queries from zone and returns a NODATA reply for
// everything else, so a name that is not in the zone reads as "no records"
// rather than as an error.
func startDNSStub(t *testing.T, zone map[string]string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { pc.Close() })

	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var parser dnsmessage.Parser
			hdr, err := parser.Start(buf[:n])
			if err != nil {
				continue
			}
			q, err := parser.Question()
			if err != nil {
				continue
			}

			b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
				ID: hdr.ID, Response: true, Authoritative: true, RecursionAvailable: true,
			})
			b.EnableCompression()
			_ = b.StartQuestions()
			_ = b.Question(q)
			_ = b.StartAnswers()

			name := strings.TrimSuffix(q.Name.String(), ".")
			if q.Type == dnsmessage.TypeA {
				if ipStr, ok := zone[name]; ok {
					if ip := net.ParseIP(ipStr).To4(); ip != nil {
						var a [4]byte
						copy(a[:], ip)
						_ = b.AResource(
							dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60},
							dnsmessage.AResource{A: a})
					}
				}
			}
			msg, err := b.Finish()
			if err != nil {
				continue
			}
			_, _ = pc.WriteTo(msg, from)
		}
	}()
	return pc.LocalAddr().String()
}

func TestDNSRunAgainstStubResolver(t *testing.T) {
	addr := startDNSStub(t, map[string]string{"nas.local": "10.0.0.5"})

	t.Run("resolves through the configured resolver", func(t *testing.T) {
		c := validSensor(model.Sensor{Name: "nas", Type: model.SensorDNS, Target: "nas.local",
			Options: model.Options{"record_type": "A", "resolver": addr}})
		if err := Validate(c); err != nil {
			t.Fatalf("sensor is not valid: %v", err)
		}
		out := Test(context.Background(), c)
		if !out.Success {
			t.Fatalf("resolution failed: %s", out.Message)
		}
		if !strings.Contains(out.Message, "10.0.0.5") {
			t.Errorf("message %q does not report the address", out.Message)
		}
	})

	t.Run("expected answer matches", func(t *testing.T) {
		c := validSensor(model.Sensor{Name: "nas", Type: model.SensorDNS, Target: "nas.local",
			Options: model.Options{"resolver": addr, "expected": "10.0.0."}})
		out := Test(context.Background(), c)
		if !out.Success {
			t.Fatalf("expected answer was not matched: %s", out.Message)
		}
	})

	t.Run("expected answer is missing", func(t *testing.T) {
		c := validSensor(model.Sensor{Name: "nas", Type: model.SensorDNS, Target: "nas.local",
			Options: model.Options{"resolver": addr, "expected": "192.168.1.1"}})
		out := Test(context.Background(), c)
		if out.Success {
			t.Fatal("a mismatched answer was accepted")
		}
		if !strings.Contains(out.Message, "missing") {
			t.Errorf("message %q does not name the missing value", out.Message)
		}
	})

	t.Run("unknown name yields no records", func(t *testing.T) {
		c := validSensor(model.Sensor{Name: "nope", Type: model.SensorDNS, Target: "absent.local",
			Options: model.Options{"resolver": addr}})
		out := Test(context.Background(), c)
		if out.Success {
			t.Fatal("an empty answer was treated as success")
		}
		if strings.TrimSpace(out.Message) == "" {
			t.Error("the failure has no message")
		}
	})
}

// ---------------------------------------------------------------------------
// registry
// ---------------------------------------------------------------------------

// TestEveryTypeHasAProber walks model.SensorTypes rather than a list of its own.
// A second list would be a second thing to forget when a type is added, and the
// forgetting would be silent: the new type would simply never be checked.
func TestEveryTypeHasAProber(t *testing.T) {
	for _, typ := range model.SensorTypes {
		c, ok := Get(typ)
		if !ok {
			t.Errorf("no prober registered for %q", typ)
			continue
		}
		if c.Type() != typ {
			t.Errorf("prober for %q reports type %q", typ, c.Type())
		}
	}
	if _, ok := Get("carrier-pigeon"); ok {
		t.Error("an unknown type resolved to a prober")
	}
}

// TestTestNeverPanics feeds garbage to every engine. A probe must always return
// an outcome: the scheduler stores it, and a panic would take the daemon down.
func TestTestNeverPanics(t *testing.T) {
	targets := []model.Sensor{
		{Name: "g", Type: model.SensorHTTP, Target: "http://127.0.0.1:1/x"},
		{Name: "g", Type: model.SensorTCP, Target: "127.0.0.1:1"},
		{Name: "g", Type: model.SensorTCP, Target: "not a target"},
		{Name: "g", Type: model.SensorDNS, Target: ""},
		{Name: "g", Type: model.SensorTLS, Target: "not a target"},
		{Name: "g", Type: model.SensorTLS, Target: "127.0.0.1:1"},
		// A socket that is certainly not there, rather than the default one: on a
		// machine that runs Docker the default would answer, and this test is
		// about surviving nonsense, not about whether Docker happens to be up.
		{Name: "g", Type: model.SensorDocker, Target: "nginx",
			Options: model.Options{"socket": filepath.Join(t.TempDir(), "absent.sock")}},
		{Name: "g", Type: model.SensorDocker, Target: ""},
		{Name: "g", Type: model.SensorSNMP, Target: "127.0.0.1:1",
			Options: model.Options{"oid": "1.3.6.1.2.1.1.3.0", "min": 0}},
		{Name: "g", Type: model.SensorSNMP, Target: "not a target",
			Options: model.Options{"oid": "1.3.6.1"}},
		{Name: "g", Type: model.SensorPing, Target: "127.0.0.1", Options: model.Options{"count": "0"}},
		{Name: "g", Type: "unknown", Target: "x"},
	}
	for _, c := range targets {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		out := Test(ctx, c)
		cancel()
		if out.Success {
			t.Errorf("%s %q unexpectedly succeeded", c.Type, c.Target)
		}
		if strings.TrimSpace(out.Message) == "" {
			t.Errorf("%s %q failed without a message", c.Type, c.Target)
		}
	}
}
