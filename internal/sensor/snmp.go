package sensor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/j000k3r/weatherglass/internal/model"
)

// SNMPProber asks one OID and checks what comes back.
//
// Deliberately one OID with one GET: no walk, no traps, no v3. A switch, a router
// or a NAS answers "how warm is the CPU", "how long has it been up" and "how much
// ink is left" with a single request each, and that is the whole job. A walk
// enumerates a device's entire MIB, which is a different tool with a different
// cost.
//
// v2c is unauthenticated and sends the community string in clear text. That is
// stated in SECURITY.md rather than buried here, because it decides which network
// the sensor belongs on.
//
// Options:
//
//	community  v2c community, default "public"
//	version    "2c" (default) or "1"
//	oid        the object to ask for, dotted, required
//	expect     substring the value must contain
//	min, max   bounds the value must lie within, for a numeric object
type SNMPProber struct{}

// Type implements Prober.
func (SNMPProber) Type() model.SensorType { return model.SensorSNMP }

// defaultSNMPPort is the well-known port of the agent.
const defaultSNMPPort = 161

// Validate implements Prober.
func (SNMPProber) Validate(c model.Sensor) error {
	if _, _, err := snmpTarget(c.Target); err != nil {
		return err
	}
	if oid, ok := c.Options["oid"]; !ok {
		return errors.New("oid is required: the object to ask for, for example 1.3.6.1.2.1.1.3.0")
	} else if oid == nil {
		return errors.New("oid must not be empty")
	} else if s, isStr := oid.(string); !isStr || strings.TrimSpace(s) == "" {
		return errors.New("oid must not be empty")
	} else if !validOID(s) {
		return fmt.Errorf("oid must be a dotted number such as 1.3.6.1.2.1.1.3.0, got %q", s)
	}

	switch v := c.Options.Str("version", "2c"); v {
	case "2c", "1":
	case "3":
		return errors.New("version 3 is not supported; use 2c")
	default:
		return fmt.Errorf("version must be 2c or 1, got %q", v)
	}

	// At least one assertion, because "the agent answered something" is what a
	// ping already tells you. The value has to be judged against something the
	// operator wrote down, or the sensor is a liveness check wearing a number.
	expect := strings.TrimSpace(c.Options.Str("expect", ""))
	min, hasMin, err := optionFloat(c.Options, "min")
	if err != nil {
		return err
	}
	max, hasMax, err := optionFloat(c.Options, "max")
	if err != nil {
		return err
	}
	if expect == "" && !hasMin && !hasMax {
		return errors.New("give at least one of expect, min or max: the sensor needs something to check the value against")
	}
	if hasMin && hasMax && min > max {
		return fmt.Errorf("min (%g) is above max (%g)", min, max)
	}
	return nil
}

// Run implements Prober.
func (SNMPProber) Run(ctx context.Context, c model.Sensor) Outcome {
	o := c.Options
	host, port, err := snmpTarget(c.Target)
	if err != nil {
		return Errf(0, "%v", err)
	}
	oid := normaliseOID(o.Str("oid", ""))
	version := gosnmp.Version2c
	versionText := "2c"
	if o.Str("version", "2c") == "1" {
		version = gosnmp.Version1
		versionText = "1"
	}

	// The probe's own timeout is the shorter of the sensor's and what is left of
	// the context, so a cancelled probe does not sit in a UDP read until the
	// sensor's full timeout has passed.
	timeout := 10 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl); left > 0 && left < timeout {
			timeout = left
		}
	}

	client := &gosnmp.GoSNMP{
		Target:    host,
		Port:      port,
		Community: o.Str("community", "public"),
		Version:   version,
		Transport: "udp",
		Timeout:   timeout,
		// One attempt. The scheduler's retries are the retry policy, and a second
		// packet sent by the library would be invisible to it.
		Retries: 0,
		// One OID per probe, and the library refuses a request for more OIDs than
		// this. It also defaults the value during Connect, but saying it here
		// means the number the request is built from is the one written down.
		MaxOids: 1,
		Context: ctx,
	}

	start := time.Now()
	if err := client.Connect(); err != nil {
		return Errf(time.Since(start), "%s", describeSNMPError(err, c.Target, timeout))
	}
	defer func() { _ = client.Conn.Close() }()

	packet, err := client.Get([]string{oid})
	latency := time.Since(start)
	if err != nil {
		return Errf(latency, "%s", describeSNMPError(err, c.Target, timeout))
	}
	if packet.Error != gosnmp.NoError {
		return Errf(latency, "the agent refused the request: %s", packet.Error)
	}
	if len(packet.Variables) == 0 {
		return Errf(latency, "the agent answered %s with no value", oid)
	}

	value := packet.Variables[0]
	switch value.Type {
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView:
		return Errf(latency, "the agent has no object at %s (%s)", oid, snmpTypeText(value.Type))
	case gosnmp.Null:
		return Errf(latency, "the agent has no value for %s (null)", oid)
	default:
		// Anything else is a value, and snmpValue below reads it. These two cases
		// are the absence of one, which has to be caught here rather than there:
		// a "no such object" printed as a value would read as an answer.
	}

	text, number, numeric := snmpValue(value)

	if want := o.Str("expect", ""); want != "" && !strings.Contains(text, want) {
		return Errf(latency, "%s is %q, expected it to contain %q", oid, text, want)
	}
	min, hasMin, err := optionFloat(o, "min")
	if err != nil {
		return Errf(latency, "%v", err)
	}
	max, hasMax, err := optionFloat(o, "max")
	if err != nil {
		return Errf(latency, "%v", err)
	}
	if hasMin || hasMax {
		if !numeric {
			return Errf(latency, "%s is %q, which is not a number, so min/max cannot judge it", oid, text)
		}
		if hasMin && number < min {
			return Errf(latency, "%s is %s, below the minimum of %g", oid, text, min)
		}
		if hasMax && number > max {
			return Errf(latency, "%s is %s, above the maximum of %g", oid, text, max)
		}
	}

	return Outcome{
		Success: true,
		Latency: latency,
		Message: fmt.Sprintf("SNMP v%s · %s = %s · %.0fms",
			versionText, oid, text, float64(latency.Microseconds())/1000),
	}
}

// snmpTarget splits a target into host and port, defaulting to the standard
// agent port. A bare host is the common case; host:port exists for an agent on a
// non-standard port.
func snmpTarget(raw string) (string, uint16, error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", 0, errors.New("target is required, in host or host:port form")
	}
	if strings.Contains(target, "://") {
		return "", 0, errors.New("target must be a host or host:port, without a scheme")
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		// No colon at all: a bare host, which is the normal way to write it.
		if strings.Contains(target, ":") {
			return "", 0, fmt.Errorf("target must be host or host:port (for example 192.168.1.2 or 192.168.1.2:161): %w", err)
		}
		host, portText = target, strconv.Itoa(defaultSNMPPort)
	}
	if strings.TrimSpace(host) == "" {
		return "", 0, errors.New("target must include a host")
	}
	if strings.Contains(host, "/") {
		return "", 0, errors.New("target must not contain a path")
	}
	badPort := fmt.Errorf("port must be a number between 1 and 65535, got %q", portText)
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, badPort
	}
	// Kept as its own check rather than folded into the error branch above: the
	// conversion to the port number below is only safe because this line exists.
	if port < 1 || port > 65535 {
		return "", 0, badPort
	}
	return host, uint16(port), nil
}

// validOID reports whether the string is a dotted number. Names are not
// accepted: resolving "sysUpTime.0" needs a MIB file, and a monitoring tool that
// silently guesses at one is a monitoring tool that quietly watches the wrong
// thing.
func validOID(oid string) bool {
	oid = normaliseOID(oid)
	if oid == "" {
		return false
	}
	parts := strings.Split(oid, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		if _, err := strconv.ParseUint(p, 10, 32); err != nil {
			return false
		}
	}
	return true
}

// normaliseOID drops the leading dot that is legal in SNMP notation but is noise
// everywhere else.
func normaliseOID(oid string) string {
	return strings.TrimPrefix(strings.TrimSpace(oid), ".")
}

// optionFloat reads a numeric option, and unlike Options.Int it also accepts a
// number written as a string. A hand-written JSON or YAML file is the likeliest
// place for `"min": "5"`, and silently dropping it would turn "the value is below
// the minimum" into "you gave no bounds", which is a much worse error.
func optionFloat(o model.Options, key string) (float64, bool, error) {
	v, ok := o[key]
	if !ok || v == nil {
		return 0, false, nil
	}
	switch n := v.(type) {
	case int:
		return float64(n), true, nil
	case int64:
		return float64(n), true, nil
	case float64:
		return n, true, nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0, false, fmt.Errorf("%s must be a number, got %q", key, n)
		}
		return f, true, nil
	}
	return 0, false, fmt.Errorf("%s must be a number", key)
}

// snmpValue renders one variable binding for a human, and separately reports it
// as a number when it is one.
func snmpValue(v gosnmp.SnmpPDU) (text string, number float64, numeric bool) {
	switch v.Type {
	case gosnmp.Integer:
		if n := gosnmp.ToBigInt(v.Value); n != nil {
			return n.String(), float64(n.Int64()), true
		}
	case gosnmp.Counter32, gosnmp.Gauge32, gosnmp.Uinteger32, gosnmp.Counter64:
		if n := gosnmp.ToBigInt(v.Value); n != nil {
			return n.String(), bigToFloat(n), true
		}
	case gosnmp.TimeTicks:
		// Hundredths of a second since the agent started — the one value that
		// everybody asks for and nobody wants to read as a raw integer.
		if n := gosnmp.ToBigInt(v.Value); n != nil {
			return fmt.Sprintf("%s (%s)", n.String(), ticksText(n.Uint64())), bigToFloat(n), true
		}
	case gosnmp.OctetString:
		switch b := v.Value.(type) {
		case []byte:
			return printableOrHex(b), 0, false
		case string:
			return b, 0, false
		}
	case gosnmp.ObjectIdentifier:
		if s, ok := v.Value.(string); ok {
			return s, 0, false
		}
	case gosnmp.IPAddress:
		if s, ok := v.Value.(string); ok {
			return s, 0, false
		}
	case gosnmp.Boolean:
		if b, ok := v.Value.(bool); ok {
			if b {
				return "true", 1, true
			}
			return "false", 0, true
		}
	case gosnmp.BitString, gosnmp.Opaque:
		if b, ok := v.Value.([]byte); ok {
			return fmt.Sprintf("0x%x", b), 0, false
		}
	default:
		// Null, NsapAddress, ObjectDescription, the opaque floats, and anything
		// this function has not been taught: the Go value is a worse rendering
		// than the ones above but never a wrong one.
		break
	}
	return fmt.Sprintf("%v", v.Value), 0, false
}

// printableOrHex keeps a readable string readable and makes anything else
// obvious as bytes. A MAC address arrives as six bytes; rendering them as
// punctuation soup would be worse than hex.
func printableOrHex(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return fmt.Sprintf("0x%x", b)
		}
	}
	return string(b)
}

// ticksText renders TimeTicks' hundredths of a second as days, hours and
// minutes: "4d 3h 12m".
//
// The arithmetic stays in uint64 rather than going through time.Duration, so
// there is no signed conversion that could overflow on a value the agent made up.
func ticksText(hundredths uint64) string {
	seconds := hundredths / 100
	minutes := seconds / 60
	hours := minutes / 60
	days := hours / 24
	hours %= 24
	minutes %= 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

// bigToFloat converts a Counter64 for the min/max comparison. A 64-bit counter
// past 2^53 loses precision in a float64, which is fine: a bound that fine is
// not what anybody writes down.
func bigToFloat(n *big.Int) float64 {
	if n == nil {
		return 0
	}
	f, _ := new(big.Float).SetInt(n).Float64()
	if math.IsInf(f, 0) {
		return 0
	}
	return f
}

// snmpTypeText names the "no value" types in words rather than by their wire
// name, because the two look identical in a dashboard and mean different things.
func snmpTypeText(t gosnmp.Asn1BER) string {
	switch t {
	case gosnmp.NoSuchObject:
		return "no such object"
	case gosnmp.NoSuchInstance:
		return "no such instance"
	case gosnmp.EndOfMibView:
		return "end of MIB view"
	default:
		// A type that does carry a value has no reason to be named in words, and
		// this function is only ever asked about the three that do not.
		break
	}
	return t.String()
}

// describeSNMPError turns a failure to get an answer into something an operator
// can act on.
//
// The commonest failure by far is silence: a wrong community string in v2c is not
// refused, it is ignored, so the sensor times out and the community is the thing
// to check. A port with nothing behind it fails the other way — the kernel's
// port-unreachable arrives as a refused read rather than as silence — and the
// answer there is different enough to be worth its own sentence.
func describeSNMPError(err error, target string, timeout time.Duration) string {
	// context.DeadlineExceeded satisfies net.Error and reports a timeout, so a
	// deadline of any origin lands here and gets the fuller sentence.
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Sprintf("no answer from %s within %s — is SNMP enabled, is the community right, and is the firewall open?",
			target, timeout.Round(time.Millisecond))
	}
	// A connected UDP socket that nobody is listening on turns the kernel's
	// port-unreachable into a refused read. From the operator's side that is the
	// same situation — something has to be switched on at the far end — but the
	// community string is not the suspect, so it is not named.
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Sprintf("nothing answered SNMP at %s — is SNMP enabled, and is the port right?", target)
	}
	return describeRequestError(err)
}
