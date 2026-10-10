package sensor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/j000k3r/weatherglass/internal/model"
)

// DNSProber resolves a name and optionally asserts the answer.
//
// Options:
//
//	record_type  A, AAAA, CNAME, MX, TXT, NS, SRV or PTR (default A)
//	resolver     query this server instead of the system resolver, for example
//	             1.1.1.1 or 192.168.1.1:53
//	expected     comma separated list of substrings the answers must contain
//	expect_all   when true every item in expected must be present, otherwise
//	             any single match is enough
type DNSProber struct{}

// Type implements Prober.
func (DNSProber) Type() model.SensorType { return model.SensorDNS }

var dnsRecordTypes = []string{"A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV", "PTR"}

// Validate implements Prober.
func (DNSProber) Validate(c model.Sensor) error {
	if strings.TrimSpace(c.Target) == "" {
		return errors.New("name to resolve is required")
	}
	if strings.ContainsAny(c.Target, " \t/") {
		return errors.New("target must be a plain DNS name")
	}
	rt := strings.ToUpper(strings.TrimSpace(c.Options.Str("record_type", "A")))
	for _, t := range dnsRecordTypes {
		if t == rt {
			return validateResolver(c.Options.Str("resolver", ""))
		}
	}
	return fmt.Errorf("unsupported record_type %q (want one of %s)", rt, strings.Join(dnsRecordTypes, ", "))
}

func validateResolver(r string) error {
	r = strings.TrimSpace(r)
	if r == "" {
		return nil
	}
	if _, _, err := net.SplitHostPort(r); err != nil {
		// Allow a bare IP; the port is filled in at query time.
		if net.ParseIP(r) == nil {
			return fmt.Errorf("resolver must be an IP address, optionally with a port, got %q", r)
		}
	}
	return nil
}

// Run implements Prober.
func (DNSProber) Run(ctx context.Context, c model.Sensor) Outcome {
	o := c.Options
	name := strings.TrimSpace(c.Target)
	rt := strings.ToUpper(strings.TrimSpace(o.Str("record_type", "A")))

	resolver := net.DefaultResolver
	if rsv := strings.TrimSpace(o.Str("resolver", "")); rsv != "" {
		if _, _, err := net.SplitHostPort(rsv); err != nil {
			rsv = net.JoinHostPort(rsv, "53")
		}
		resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, rsv)
			},
		}
	}

	start := time.Now()
	answers, err := lookupRecords(ctx, resolver, rt, name)
	latency := time.Since(start)
	if err != nil {
		return Errf(latency, "%s", describeDNSError(err))
	}
	if len(answers) == 0 {
		return Outcome{
			Success: false,
			Latency: latency,
			Message: fmt.Sprintf("%s %s · no records returned", rt, name),
			Err:     errors.New("empty answer"),
		}
	}

	expected := splitList(o.Str("expected", ""))
	if len(expected) > 0 {
		expectAll := o.Bool("expect_all", false)
		found := 0
		var missing []string
		for _, want := range expected {
			hit := false
			for _, a := range answers {
				if strings.Contains(strings.ToLower(a), strings.ToLower(want)) {
					hit = true
					break
				}
			}
			if hit {
				found++
			} else {
				missing = append(missing, want)
			}
		}
		if (expectAll && found != len(expected)) || (!expectAll && found == 0) {
			return Outcome{
				Success: false,
				Latency: latency,
				Message: fmt.Sprintf("%s %s · missing %s (got %s)",
					rt, name, strings.Join(missing, ", "), truncate(strings.Join(answers, ", "), 90)),
				Err: errors.New("answer mismatch"),
			}
		}
	}

	sort.Strings(answers)
	return Outcome{
		Success: true,
		Latency: latency,
		Message: fmt.Sprintf("%s %s · %s · %.0fms",
			rt, name, truncate(strings.Join(answers, ", "), 110),
			float64(latency.Microseconds())/1000),
	}
}

// lookupRecords resolves one record type and returns the answers as strings.
// Answer TTLs are deliberately not reported: net.Resolver does not expose them.
func lookupRecords(ctx context.Context, r *net.Resolver, recordType, name string) ([]string, error) {
	switch recordType {
	case "A", "AAAA":
		// The record type is what decides the family, and this switch is only
		// ever reached with one of the two. net.Resolver answers either query
		// with both families, so this filter is what makes the result match what
		// was asked for.
		network := "ip6"
		if recordType == "A" {
			network = "ip4"
		}
		addrs, err := r.LookupIPAddr(ctx, name)
		if err != nil {
			return nil, err
		}
		out := []string{}
		for _, a := range addrs {
			if network == "ip4" && a.IP.To4() == nil {
				continue
			}
			if network == "ip6" && a.IP.To4() != nil {
				continue
			}
			out = append(out, a.IP.String())
		}
		return out, nil

	case "CNAME":
		cname, err := r.LookupCNAME(ctx, name)
		if err != nil {
			return nil, err
		}
		return []string{cname}, nil

	case "MX":
		recs, err := r.LookupMX(ctx, name)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(recs))
		for _, m := range recs {
			out = append(out, fmt.Sprintf("%d %s", m.Pref, strings.TrimSuffix(m.Host, ".")))
		}
		return out, nil

	case "TXT":
		recs, err := r.LookupTXT(ctx, name)
		if err != nil {
			return nil, err
		}
		return recs, nil

	case "NS":
		recs, err := r.LookupNS(ctx, name)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(recs))
		for _, n := range recs {
			out = append(out, strings.TrimSuffix(n.Host, "."))
		}
		return out, nil

	case "SRV":
		_, recs, err := r.LookupSRV(ctx, "", "", name)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(recs))
		for _, s := range recs {
			out = append(out, fmt.Sprintf("%s:%d", strings.TrimSuffix(s.Target, "."), s.Port))
		}
		return out, nil

	case "PTR":
		recs, err := r.LookupAddr(ctx, name)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(recs))
		for _, p := range recs {
			out = append(out, strings.TrimSuffix(p, "."))
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported record type %q", recordType)
}

func describeDNSError(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return "no such host (NXDOMAIN)"
		}
		if dnsErr.IsTimeout {
			return "DNS query timed out"
		}
		return "DNS: " + dnsErr.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "DNS query timed out"
	}
	return err.Error()
}

func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
