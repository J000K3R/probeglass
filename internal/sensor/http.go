package sensor

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// HTTPProber probes an HTTP or HTTPS endpoint.
//
// Options:
//
//	method               request method, default GET
//	expected_status      "200", "200-299", "2xx" or a comma separated list,
//	                     default 200-399
//	headers              map of request headers
//	user_agent           overrides the User-Agent header
//	basic_auth_user      username for HTTP basic auth
//	basic_auth_pass      password for HTTP basic auth
//	bearer_token         value for an Authorization: Bearer header
//	body                 request body for POST/PUT/PATCH
//	body_contains        substring the response body must contain
//	body_not_contains    substring the response body must not contain
//	body_regex           regular expression the response body must match
//	expected_headers     map of response headers that must contain a substring
//	max_body_bytes       how much of the body to read, default 262144
//	follow_redirects     default true
//	insecure_skip_verify default false; accepts self-signed certificates
//	cert_warn_days       fail when the TLS certificate expires within N days
type HTTPProber struct{}

// Type implements Prober.
func (HTTPProber) Type() model.SensorType { return model.SensorHTTP }

// statusSpec is a set of accepted status code ranges.
type statusSpec [][2]int

// parseStatusSpec understands "200", "204,301", "200-299" and "2xx".
func parseStatusSpec(s string) (statusSpec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return statusSpec{{200, 399}}, nil
	}
	var spec statusSpec
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		if part == "" {
			continue
		}
		switch {
		case strings.HasSuffix(part, "xx") && len(part) == 3:
			d, err := strconv.Atoi(part[:1])
			if err != nil || d < 1 || d > 5 {
				return nil, fmt.Errorf("invalid status spec %q", part)
			}
			spec = append(spec, [2]int{d * 100, d*100 + 99})
		case strings.Contains(part, "-"):
			lo, hi, ok := strings.Cut(part, "-")
			a, err1 := strconv.Atoi(strings.TrimSpace(lo))
			b, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if !ok || err1 != nil || err2 != nil || a < 100 || b > 599 || a > b {
				return nil, fmt.Errorf("invalid status range %q", part)
			}
			spec = append(spec, [2]int{a, b})
		default:
			n, err := strconv.Atoi(part)
			if err != nil || n < 100 || n > 599 {
				return nil, fmt.Errorf("invalid status code %q", part)
			}
			spec = append(spec, [2]int{n, n})
		}
	}
	if len(spec) == 0 {
		return nil, fmt.Errorf("empty status spec")
	}
	return spec, nil
}

func (s statusSpec) match(code int) bool {
	for _, r := range s {
		if code >= r[0] && code <= r[1] {
			return true
		}
	}
	return false
}

func (s statusSpec) String() string {
	parts := make([]string, 0, len(s))
	for _, r := range s {
		if r[0] == r[1] {
			parts = append(parts, strconv.Itoa(r[0]))
			continue
		}
		parts = append(parts, fmt.Sprintf("%d-%d", r[0], r[1]))
	}
	return strings.Join(parts, ",")
}

// Validate implements Prober.
func (HTTPProber) Validate(c model.Sensor) error {
	if c.Target == "" {
		return errors.New("target URL is required")
	}
	u, err := url.Parse(c.Target)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("URL must include a host")
	}
	if _, err := parseStatusSpec(c.Options.Str("expected_status", "")); err != nil {
		return err
	}
	if re := c.Options.Str("body_regex", ""); re != "" {
		if _, err := regexp.Compile(re); err != nil {
			return fmt.Errorf("body_regex is not a valid regular expression: %w", err)
		}
	}
	switch m := strings.ToUpper(c.Options.Str("method", http.MethodGet)); m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
	default:
		return fmt.Errorf("unsupported method %q", m)
	}
	if c.Options.Int("cert_warn_days", 0) < 0 {
		return errors.New("cert_warn_days must not be negative")
	}
	if c.Options.Int("max_body_bytes", 0) < 0 {
		return errors.New("max_body_bytes must not be negative")
	}
	return nil
}

var (
	transportOnce     sync.Once
	transportSecure   *http.Transport
	transportInsecure *http.Transport
)

func newHTTPTransport(insecure bool) *http.Transport {
	t := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if insecure {
		// Only ever reachable when the user explicitly asked for it on a sensor.
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	return t
}

func httpTransport(insecure bool) *http.Transport {
	transportOnce.Do(func() {
		transportSecure = newHTTPTransport(false)
		transportInsecure = newHTTPTransport(true)
	})
	if insecure {
		return transportInsecure
	}
	return transportSecure
}

// Run implements Prober.
func (HTTPProber) Run(ctx context.Context, c model.Sensor) Outcome {
	o := c.Options

	method := strings.ToUpper(strings.TrimSpace(o.Str("method", http.MethodGet)))
	if method == "" {
		method = http.MethodGet
	}

	var bodyReader io.Reader
	if payload := o.Str("body", ""); payload != "" && method != http.MethodGet && method != http.MethodHead {
		bodyReader = strings.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.Target, bodyReader)
	if err != nil {
		return Errf(0, "build request: %v", err)
	}
	for k, v := range o.StrMap("headers") {
		req.Header.Set(k, v)
	}
	if ua := o.Str("user_agent", "ProbeGlass/"+model.Version); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if tok := o.Str("bearer_token", ""); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if user := o.Str("basic_auth_user", ""); user != "" {
		req.SetBasicAuth(user, o.Str("basic_auth_pass", ""))
	}
	if bodyReader != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	follow := o.Bool("follow_redirects", true)
	client := &http.Client{
		Transport: httpTransport(o.Bool("insecure_skip_verify", false)),
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if !follow {
				return http.ErrUseLastResponse
			}
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		},
	}

	start := time.Now()
	// The target is the sensor: an administrator chooses it, and an HTTP sensor
	// that refused private addresses could not watch the homelab it exists for.
	// codeql[go/request-forgery]
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return Errf(latency, "%s", describeRequestError(err))
	}
	defer resp.Body.Close()

	limit := int64(o.Int("max_body_bytes", 256*1024))
	if limit <= 0 {
		limit = 256 * 1024
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, limit))
	if readErr != nil {
		return Errf(latency, "read response body: %v", readErr)
	}
	// Drain a little more so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))

	ms := float64(latency.Microseconds()) / 1000
	code := resp.StatusCode

	// Certificate expiry is checked before the status so that an expiring
	// certificate is reported even when the endpoint still answers.
	certNote := ""
	if resp.TLS != nil {
		if ci, ok := leafInfo(*resp.TLS); ok {
			certNote = ci.note()
			if ci.warning(o.Int("cert_warn_days", 0)) {
				return Outcome{
					Success:    false,
					Latency:    latency,
					StatusCode: code,
					Message:    ci.describe(),
					Err:        errors.New("tls certificate expires soon"),
				}
			}
		}
	}

	spec, err := parseStatusSpec(o.Str("expected_status", ""))
	if err != nil {
		return Errf(latency, "invalid expected_status: %v", err)
	}
	if !spec.match(code) {
		return Outcome{
			Success:    false,
			Latency:    latency,
			StatusCode: code,
			Message:    fmt.Sprintf("HTTP %d, expected %s", code, spec.String()),
			Err:        fmt.Errorf("unexpected status code %d", code),
		}
	}

	text := string(data)
	if want := o.Str("body_contains", ""); want != "" && !strings.Contains(text, want) {
		return Outcome{
			Success:    false,
			Latency:    latency,
			StatusCode: code,
			Message:    fmt.Sprintf("HTTP %d but body does not contain %q", code, want),
			Err:        errors.New("body content mismatch"),
		}
	}
	if unwanted := o.Str("body_not_contains", ""); unwanted != "" && strings.Contains(text, unwanted) {
		return Outcome{
			Success:    false,
			Latency:    latency,
			StatusCode: code,
			Message:    fmt.Sprintf("HTTP %d but body contains %q", code, unwanted),
			Err:        errors.New("body content mismatch"),
		}
	}
	if pattern := o.Str("body_regex", ""); pattern != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return Errf(latency, "invalid body_regex: %v", err)
		}
		if !re.MatchString(text) {
			return Outcome{
				Success:    false,
				Latency:    latency,
				StatusCode: code,
				Message:    fmt.Sprintf("HTTP %d but body does not match /%s/", code, pattern),
				Err:        errors.New("body content mismatch"),
			}
		}
	}
	for header, want := range o.StrMap("expected_headers") {
		got := resp.Header.Get(header)
		if got == "" {
			return Outcome{
				Success:    false,
				Latency:    latency,
				StatusCode: code,
				Message:    fmt.Sprintf("HTTP %d but response header %s is missing", code, header),
				Err:        errors.New("missing response header"),
			}
		}
		if !strings.Contains(got, want) {
			return Outcome{
				Success:    false,
				Latency:    latency,
				StatusCode: code,
				Message:    fmt.Sprintf("HTTP %d but %s is %q, expected to contain %q", code, header, got, want),
				Err:        errors.New("response header mismatch"),
			}
		}
	}

	note := strings.TrimSpace(http.StatusText(code))
	if note != "" {
		note = " " + note
	}
	return Outcome{
		Success:    true,
		Latency:    latency,
		StatusCode: code,
		Message:    fmt.Sprintf("HTTP %d%s · %.0fms%s", code, note, ms, certNote),
	}
}

// describeRequestError turns nested transport errors into something a homelab
// operator can act on.
func describeRequestError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) && uerr.Err != nil {
		err = uerr.Err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout exceeded"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return "DNS: host not found"
		}
		return "DNS: " + dnsErr.Err
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Timeout() {
			return "connection timed out"
		}
		if opErr.Op == "dial" {
			return "connection refused or unreachable"
		}
	}
	// A refused certificate is said out loud with its reason: "not trusted"
	// covers a wrong name, an expiry and a private CA, and those three are
	// fixed in three different ways.
	if msg, ok := describeCertError(err); ok {
		return msg
	}
	msg := err.Error()
	if msg == "" {
		return "request failed"
	}
	return msg
}
