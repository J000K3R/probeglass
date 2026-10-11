package sensor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/j000k3r/probeglass/internal/model"
)

// ---------------------------------------------------------------------------
// the stub
//
// A real UDP agent: it listens, unpacks the request with the same library the
// prober uses, and answers with fixed varbinds. Nothing is mocked — if the
// prober asked for the wrong OID, sent the wrong community or could not read the
// answer, this is a real agent that would not have replied the way the test
// expects.
// ---------------------------------------------------------------------------

// snmpAgent is a one-OID SNMP agent for tests.
type snmpAgent struct {
	mu sync.Mutex
	// asked is every OID the prober requested, in order.
	asked []string
	// communities is every community string the prober arrived with.
	communities []string

	vars   []gosnmp.SnmpPDU
	code   gosnmp.SNMPError
	silent bool

	// decodeErr records a request the agent could not parse. It is kept rather
	// than reported from the goroutine, because t.Errorf after a test has
	// finished takes the whole run down.
	decodeErr error
}

// newSNMPAgent starts an agent on a free UDP port and returns it with its address.
func newSNMPAgent(t *testing.T, vars ...gosnmp.SnmpPDU) (*snmpAgent, string) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	a := &snmpAgent{vars: vars}
	go a.serve(conn)
	return a, conn.LocalAddr().String()
}

func (a *snmpAgent) serve(conn *net.UDPConn) {
	parser := &gosnmp.GoSNMP{}
	buf := make([]byte, 65535)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return // the socket was closed at the end of the test
		}
		req, err := parser.UnmarshalTrap(buf[:n], false)
		if err != nil {
			a.mu.Lock()
			a.decodeErr = err
			a.mu.Unlock()
			continue
		}

		a.mu.Lock()
		for _, v := range req.Variables {
			a.asked = append(a.asked, normaliseOID(v.Name))
		}
		a.communities = append(a.communities, req.Community)
		silent, vars, code := a.silent, a.vars, a.code
		a.mu.Unlock()

		if silent {
			// A real agent with the wrong community does not answer at all.
			continue
		}

		resp := &gosnmp.SnmpPacket{
			Version:   req.Version,
			Community: req.Community,
			PDUType:   gosnmp.GetResponse,
			RequestID: req.RequestID,
			Error:     code,
			Variables: vars,
		}
		out, err := resp.MarshalMsg()
		if err != nil {
			a.mu.Lock()
			a.decodeErr = err
			a.mu.Unlock()
			continue
		}
		_, _ = conn.WriteToUDP(out, from)
	}
}

func (a *snmpAgent) seen() (oids, communities []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.asked...), append([]string(nil), a.communities...)
}

// silentAgent is an agent that accepts requests and never answers.
func silentAgent(t *testing.T) string {
	t.Helper()
	a, addr := newSNMPAgent(t)
	a.mu.Lock()
	a.silent = true
	a.mu.Unlock()
	return addr
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// oid is the object every test asks for unless it says otherwise: sysUpTime.0.
const uptimeOID = "1.3.6.1.2.1.1.3.0"

func number(v int) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: uptimeOID, Type: gosnmp.Integer, Value: v}
}

func text(s string) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: uptimeOID, Type: gosnmp.OctetString, Value: []byte(s)}
}

func ticks(n uint32) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: uptimeOID, Type: gosnmp.TimeTicks, Value: n}
}

func snmpSensor(target string, opts model.Options) model.Sensor {
	merged := model.Options{"oid": uptimeOID}
	for k, v := range opts {
		merged[k] = v
	}
	return model.Sensor{
		Name: "switch", Type: model.SensorSNMP, Target: target,
		Interval: 60, Timeout: 10, Retries: 1, Enabled: true, Options: merged,
	}
}

func runSNMP(t *testing.T, timeout time.Duration, c model.Sensor) Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return SNMPProber{}.Run(ctx, c)
}

// ---------------------------------------------------------------------------
// reading a value
// ---------------------------------------------------------------------------

func TestSNMPRunReadsATimeTicksValue(t *testing.T) {
	// 429500 hundredths of a second is 1h 11m 35s.
	agent, addr := newSNMPAgent(t, ticks(429500))

	out := runSNMP(t, 5*time.Second, snmpSensor(addr, model.Options{"min": 1}))
	if !out.Success {
		t.Fatalf("a good answer was reported as a failure: %s", out.Message)
	}
	for _, want := range []string{"SNMP v2c", uptimeOID, "429500", "1h 11m"} {
		if !strings.Contains(out.Message, want) {
			t.Errorf("message %q does not contain %q", out.Message, want)
		}
	}
	if oids, _ := agent.seen(); len(oids) != 1 || oids[0] != uptimeOID {
		t.Errorf("the agent was asked %v, want one GET for %s", oids, uptimeOID)
	}
}

func TestSNMPRunMatchesAnExpectedSubstring(t *testing.T) {
	_, addr := newSNMPAgent(t, text("Routerboard RB5009"))

	out := runSNMP(t, 5*time.Second, snmpSensor(addr, model.Options{"expect": "RB5009"}))
	if !out.Success {
		t.Fatalf("a matching value was reported as a failure: %s", out.Message)
	}
	if !strings.Contains(out.Message, "Routerboard RB5009") {
		t.Errorf("message %q should show the value it read", out.Message)
	}
}

func TestSNMPRunFailsWhenTheValueDoesNotMatch(t *testing.T) {
	_, addr := newSNMPAgent(t, text("Routerboard RB5009"))

	out := runSNMP(t, 5*time.Second, snmpSensor(addr, model.Options{"expect": "cisco"}))
	if out.Success {
		t.Fatalf("a value that does not match was accepted: %s", out.Message)
	}
	if !strings.Contains(out.Message, "RB5009") || !strings.Contains(out.Message, "cisco") {
		t.Errorf("message %q should show both what was read and what was expected", out.Message)
	}
}

func TestSNMPRunKeepsANonPrintableValueReadable(t *testing.T) {
	// A MAC address arrives as six raw bytes. Showing them as punctuation soup
	// would be worse than hex, and showing them as a string would corrupt them.
	_, addr := newSNMPAgent(t, gosnmp.SnmpPDU{
		Name: uptimeOID, Type: gosnmp.OctetString,
		Value: []byte{0x00, 0x1a, 0x2b, 0x3c, 0x4d, 0x5e},
	})

	out := runSNMP(t, 5*time.Second, snmpSensor(addr, model.Options{"expect": "0x"}))
	if !out.Success {
		t.Fatalf("unexpected failure: %s", out.Message)
	}
	if !strings.Contains(out.Message, "0x001a2b3c4d5e") {
		t.Errorf("message %q should show the bytes as hex", out.Message)
	}
}

// ---------------------------------------------------------------------------
// the bounds
// ---------------------------------------------------------------------------

func TestSNMPRunChecksTheBounds(t *testing.T) {
	cases := []struct {
		name  string
		value int
		opts  model.Options
		want  bool
	}{
		{"inside", 45, model.Options{"min": 30, "max": 60}, true},
		{"on the minimum", 30, model.Options{"min": 30}, true},
		{"on the maximum", 60, model.Options{"max": 60}, true},
		{"below", 12, model.Options{"min": 30}, false},
		{"above", 91, model.Options{"max": 60}, false},
	}
	for _, tc := range cases {
		_, addr := newSNMPAgent(t, number(tc.value))
		out := runSNMP(t, 5*time.Second, snmpSensor(addr, tc.opts))
		if out.Success != tc.want {
			t.Errorf("%s: success = %v, want %v (message: %s)", tc.name, out.Success, tc.want, out.Message)
		}
	}
}

func TestSNMPRunRefusesToJudgeAStringBySize(t *testing.T) {
	_, addr := newSNMPAgent(t, text("Routerboard"))

	out := runSNMP(t, 5*time.Second, snmpSensor(addr, model.Options{"min": 3}))
	if out.Success {
		t.Fatalf("a string was compared against a number: %s", out.Message)
	}
	if !strings.Contains(out.Message, "not a number") {
		t.Errorf("message %q should say why it cannot judge the value", out.Message)
	}
}

// ---------------------------------------------------------------------------
// what an agent answers instead of a value
// ---------------------------------------------------------------------------

func TestSNMPRunOnAnObjectTheAgentDoesNotHave(t *testing.T) {
	_, addr := newSNMPAgent(t, gosnmp.SnmpPDU{Name: uptimeOID, Type: gosnmp.NoSuchObject})

	out := runSNMP(t, 5*time.Second, snmpSensor(addr, model.Options{"min": 0}))
	if out.Success {
		t.Fatalf("a missing object was reported as up: %s", out.Message)
	}
	if !strings.Contains(out.Message, "no object") {
		t.Errorf("message %q should say the agent has nothing there", out.Message)
	}
}

func TestSNMPRunOnAnAgentThatRefuses(t *testing.T) {
	agent, addr := newSNMPAgent(t, number(0))
	agent.mu.Lock()
	agent.code = gosnmp.NoAccess
	agent.mu.Unlock()

	out := runSNMP(t, 5*time.Second, snmpSensor(addr, model.Options{"min": 0}))
	if out.Success {
		t.Fatalf("a refusal was reported as up: %s", out.Message)
	}
	if !strings.Contains(out.Message, "NoAccess") {
		t.Errorf("message %q should name the error the agent reported", out.Message)
	}
}

func TestSNMPRunOnASilentAgent(t *testing.T) {
	addr := silentAgent(t)

	out := runSNMP(t, 400*time.Millisecond, snmpSensor(addr, model.Options{"min": 0}))
	if out.Success {
		t.Fatalf("silence was reported as up: %s", out.Message)
	}
	// A wrong community in v2c is not refused, it is ignored, so the timeout is
	// the message that has to point at the community.
	if !strings.Contains(out.Message, "no answer from") || !strings.Contains(out.Message, "community") {
		t.Errorf("message %q should point at the community string", out.Message)
	}
}

func TestSNMPRunOnAnUnreachableTarget(t *testing.T) {
	// Nothing listens here, and a connected UDP socket on a host that answers
	// with an ICMP port-unreachable fails immediately rather than timing out.
	out := runSNMP(t, 500*time.Millisecond,
		snmpSensor("127.0.0.1:1", model.Options{"min": 0}))
	if out.Success {
		t.Fatalf("an unreachable agent was reported as up: %s", out.Message)
	}
	// The operator gets a sentence about the far end, not the socket's own words.
	// "read udp 127.0.0.1:53911->127.0.0.1:1: recvfrom: connection refused" is
	// true and useless: it names nothing that can be switched on.
	if !strings.Contains(out.Message, "nothing answered SNMP at") {
		t.Errorf("message %q should say nothing answered, in words an operator can use", out.Message)
	}
	if strings.Contains(out.Message, "read udp") || strings.Contains(out.Message, "recvfrom") {
		t.Errorf("message %q leaks the raw socket error", out.Message)
	}
}

// TestSNMPDescribeError pins the wording of the classification itself, in the
// two cases the live tests above cannot both reach on every platform: a silent
// agent and a refused port look alike to the operator and are told apart only
// by which sentence comes out.
func TestSNMPDescribeError(t *testing.T) {
	const target = "192.168.1.2:161"

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "silence is about the community",
			err:  &net.OpError{Op: "read", Net: "udp", Err: os.ErrDeadlineExceeded},
			want: "no answer from 192.168.1.2:161 within 5s — is SNMP enabled, is the community right, and is the firewall open?",
		},
		{
			// The shape gosnmp hands back when the kernel's port-unreachable is
			// reported as a refused read (marshal.go wraps the socket error).
			name: "a refused port is about the port",
			err: fmt.Errorf("error reading from socket: %w",
				&net.OpError{Op: "read", Net: "udp", Err: syscall.ECONNREFUSED}),
			want: "nothing answered SNMP at 192.168.1.2:161 — is SNMP enabled, and is the port right?",
		},
		{
			name: "anything else keeps its own words",
			err:  errors.New("cannot marshal the request: bad OID"),
			want: "cannot marshal the request: bad OID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describeSNMPError(tt.err, target, 5*time.Second); got != tt.want {
				t.Errorf("describeSNMPError() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// what the prober sends
// ---------------------------------------------------------------------------

func TestSNMPRunSendsTheCommunityItWasGiven(t *testing.T) {
	agent, addr := newSNMPAgent(t, number(1))

	out := runSNMP(t, 5*time.Second,
		snmpSensor(addr, model.Options{"community": "homelab-ro", "min": 0}))
	if !out.Success {
		t.Fatalf("unexpected failure: %s", out.Message)
	}
	if _, communities := agent.seen(); len(communities) != 1 || communities[0] != "homelab-ro" {
		t.Errorf("the agent saw communities %v, want [homelab-ro]", communities)
	}
	// The community is a shared secret; it has no business in a dashboard
	// tooltip, a stored result or a log line.
	if strings.Contains(out.Message, "homelab-ro") {
		t.Errorf("message %q leaks the community string", out.Message)
	}
}

func TestSNMPRunUsesTheDefaultCommunityAndOid(t *testing.T) {
	agent, addr := newSNMPAgent(t, number(1))

	c := snmpSensor(addr, nil)
	c.Options = model.Options{"oid": uptimeOID, "expect": "1"}
	out := runSNMP(t, 5*time.Second, c)
	if !out.Success {
		t.Fatalf("unexpected failure: %s", out.Message)
	}
	if _, communities := agent.seen(); len(communities) != 1 || communities[0] != "public" {
		t.Errorf("the agent saw communities %v, want the default [public]", communities)
	}
}

func TestSNMPRunAcceptsAnOidWithALeadingDot(t *testing.T) {
	// ".1.3.6.1.2.1.1.3.0" is legal everywhere SNMP is written down, and it means
	// the same object. Refusing it would be pedantry with no upside.
	agent, addr := newSNMPAgent(t, number(1))

	c := snmpSensor(addr, nil)
	c.Options = model.Options{"oid": "." + uptimeOID, "expect": "1"}
	out := runSNMP(t, 5*time.Second, c)
	if !out.Success {
		t.Fatalf("an OID with a leading dot was refused: %s", out.Message)
	}
	if oids, _ := agent.seen(); len(oids) != 1 || oids[0] != uptimeOID {
		t.Errorf("the agent was asked %v, want %s", oids, uptimeOID)
	}
}

func TestSNMPRunSpeaksV1WhenAsked(t *testing.T) {
	_, addr := newSNMPAgent(t, number(1))

	out := runSNMP(t, 5*time.Second,
		snmpSensor(addr, model.Options{"version": "1", "min": 0}))
	if !out.Success {
		t.Fatalf("unexpected failure: %s", out.Message)
	}
	if !strings.Contains(out.Message, "SNMP v1") {
		t.Errorf("message %q should say which version answered", out.Message)
	}
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

func TestSNMPValidate(t *testing.T) {
	cases := []struct {
		name   string
		target string
		opts   model.Options
		want   string
	}{
		{"no target", "", model.Options{"oid": uptimeOID, "min": 0}, "target is required"},
		{"a scheme", "snmp://192.168.1.2", model.Options{"oid": uptimeOID, "min": 0}, "without a scheme"},
		{"a bad port", "192.168.1.2:99999", model.Options{"oid": uptimeOID, "min": 0}, "port must be"},
		{"no oid", "192.168.1.2", model.Options{"min": 0}, "oid is required"},
		{"an empty oid", "192.168.1.2", model.Options{"oid": "  ", "min": 0}, "oid must not be empty"},
		{"a named oid", "192.168.1.2", model.Options{"oid": "sysUpTime.0", "min": 0}, "dotted number"},
		{"a truncated oid", "192.168.1.2", model.Options{"oid": "1.3.", "min": 0}, "dotted number"},
		{"v3", "192.168.1.2", model.Options{"oid": uptimeOID, "version": "3", "min": 0}, "not supported"},
		{"an unknown version", "192.168.1.2", model.Options{"oid": uptimeOID, "version": "2", "min": 0}, "version must be"},
		{"no assertion at all", "192.168.1.2", model.Options{"oid": uptimeOID}, "at least one of expect"},
		{"a bound that is not a number", "192.168.1.2", model.Options{"oid": uptimeOID, "min": "warm"}, "must be a number"},
		{"a range that is inside out", "192.168.1.2", model.Options{"oid": uptimeOID, "min": 10, "max": 5}, "above max"},
	}
	for _, tc := range cases {
		c := model.Sensor{Name: "switch", Type: model.SensorSNMP, Target: tc.target,
			Interval: 60, Timeout: 10, Retries: 1, Options: tc.opts}
		err := (SNMPProber{}).Validate(c)
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v does not mention %q", tc.name, err, tc.want)
		}
	}
}

func TestSNMPValidateAcceptsThePlainForm(t *testing.T) {
	c := snmpSensor("192.168.1.2", model.Options{"oid": uptimeOID, "expect": "1"})
	if err := (SNMPProber{}).Validate(c); err != nil {
		t.Fatalf("the plain configuration was rejected: %v", err)
	}
	// The port may be written out, and then it has to be honoured.
	c.Target = "192.168.1.2:1611"
	if err := (SNMPProber{}).Validate(c); err != nil {
		t.Fatalf("host:port was rejected: %v", err)
	}
}

func TestSNMPTargetDefaultsThePort(t *testing.T) {
	host, port, err := snmpTarget("switch.lan")
	if err != nil || host != "switch.lan" || port != 161 {
		t.Errorf("snmpTarget(\"switch.lan\") = %q, %d, %v; want switch.lan, 161, nil", host, port, err)
	}
	host, port, err = snmpTarget("switch.lan:1161")
	if err != nil || host != "switch.lan" || port != 1161 {
		t.Errorf("snmpTarget(\"switch.lan:1161\") = %q, %d, %v; want switch.lan, 1161, nil", host, port, err)
	}
	if _, _, err := snmpTarget("fe80::1"); err == nil {
		t.Error("a bare IPv6 address without brackets should be refused rather than guessed at")
	}
}

// ---------------------------------------------------------------------------
// the small pieces
// ---------------------------------------------------------------------------

func TestSNMPOidValidation(t *testing.T) {
	// Whitespace around it is trimmed rather than refused: it comes from a form
	// field, and a stray space is not an ambiguity about which object is meant.
	good := []string{"1.3.6.1.2.1.1.3.0", ".1.3.6.1", "1.3", "1.3.6.1.2.1.1.3.0 "}
	bad := []string{"", ".", "1", "1..3", "1.3.x", "sysUpTime.0", "-1.3"}
	for _, oid := range good {
		if !validOID(oid) {
			t.Errorf("%q should be a valid OID", oid)
		}
	}
	for _, oid := range bad {
		if validOID(oid) {
			t.Errorf("%q should not be a valid OID", oid)
		}
	}
}

func TestSNMPTicksText(t *testing.T) {
	cases := []struct {
		hundredths uint64
		want       string
	}{
		{0, "0m"},
		{100, "0m"},
		{6000, "1m"},
		{360000, "1h 0m"},
		{429500, "1h 11m"},
		{100*3600*24 + 360000, "1d 1h 0m"},
	}
	for _, tc := range cases {
		if got := ticksText(tc.hundredths); got != tc.want {
			t.Errorf("ticksText(%d) = %q, want %q", tc.hundredths, got, tc.want)
		}
	}
}

func TestSNMPPrintableOrHex(t *testing.T) {
	if got := printableOrHex([]byte("eth0")); got != "eth0" {
		t.Errorf("printableOrHex(\"eth0\") = %q", got)
	}
	if got := printableOrHex([]byte("")); got != "" {
		t.Errorf("an empty string should stay empty, got %q", got)
	}
	if got := printableOrHex([]byte{0x00, 0xff}); got != "0x00ff" {
		t.Errorf("printableOrHex(bytes) = %q, want 0x00ff", got)
	}
}

func TestSNMPDecodeErrorIsReported(t *testing.T) {
	// A datagram that is not SNMP at all must not take the agent down, and it
	// must not be mistaken for an answer either. The prober is not involved in
	// this one: it checks the stub itself stays honest.
	agent, addr := newSNMPAgent(t, number(1))
	conn, err := net.Dial("udp4", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("this is not SNMP")); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	agent.mu.Lock()
	got := agent.decodeErr
	agent.mu.Unlock()
	if got == nil {
		t.Error("the stub should have recorded that it could not parse the datagram")
	}

	// And it is still answering afterwards.
	out := runSNMP(t, 5*time.Second, snmpSensor(addr, model.Options{"min": 0}))
	if !out.Success {
		t.Fatalf("the agent stopped answering after a bad datagram: %s", out.Message)
	}
}

func TestSNMPType(t *testing.T) {
	if got := (SNMPProber{}).Type(); got != model.SensorSNMP {
		t.Errorf("Type() = %q, want %q", got, model.SensorSNMP)
	}
}
