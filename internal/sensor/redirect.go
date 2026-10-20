package sensor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// RedirectProber walks an HTTP redirect chain and reports what it found. The
// ordinary HTTP sensor either follows redirects silently or refuses them; this
// one exists for the question "where does this address actually end up?", which
// matters when a certificate change, a domain move or a captive portal quietly
// rewrites the answer.
//
// Options:
//
//	max_hops         redirects to follow before giving up, default 10
//	expected_final   substring the final URL must contain
//	expected_status  status codes the final response may carry, default 200-299
//	insecure_skip_verify accept self-signed certificates
type RedirectProber struct{}

// Type implements Prober.
func (RedirectProber) Type() model.SensorType { return model.SensorRedirect }

// Validate implements Prober.
func (RedirectProber) Validate(c model.Sensor) error {
	u, err := url.Parse(strings.TrimSpace(c.Target))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("URL must include a host")
	}
	if n := c.Options.Int("max_hops", 10); n < 1 || n > 30 {
		return fmt.Errorf("max_hops must be between 1 and 30, got %d", n)
	}
	if _, err := parseStatusSpec(finalStatusSpec(c.Options)); err != nil {
		return err
	}
	if len(c.Options.Str("expected_final", "")) > 500 {
		return errors.New("expected_final must be at most 500 characters")
	}
	return nil
}

// finalStatusSpec is the status rule for the last page. An option that is
// present but empty reads as the default, because a configuration file that
// writes `expected_status: ""` means "I did not choose one" rather than "accept
// 2xx, 3xx and 4xx", which is what parseStatusSpec's own empty case means.
func finalStatusSpec(o model.Options) string {
	if s := strings.TrimSpace(o.Str("expected_status", "200-299")); s != "" {
		return s
	}
	return "200-299"
}

// Run implements Prober.
func (RedirectProber) Run(ctx context.Context, c model.Sensor) Outcome {
	maxHops := c.Options.Int("max_hops", 10)
	if maxHops < 1 {
		maxHops = 1
	}
	if maxHops > 30 {
		maxHops = 30
	}

	client := &http.Client{
		Transport: httpTransport(c.Options.Bool("insecure_skip_verify", false)),
		// Every hop is a request this prober makes itself, so that the chain can
		// be reported rather than collapsed into the last response.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	current := strings.TrimSpace(c.Target)
	var final string
	codes := make([]string, 0, maxHops+1)
	start := time.Now()

	var finalCode int
	for hops := 0; ; hops++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return Errf(time.Since(start), "build request: %v", err)
		}
		req.Header.Set("User-Agent", "ProbeGlass/"+model.Version)

		// The target is the sensor's whole purpose: an administrator chooses the
		// address, and this prober requests exactly that address and the places it
		// redirects to. Private addresses cannot be refused — the homelab is where
		// the sensors point.
		// codeql[go/request-forgery]
		resp, err := client.Do(req)
		if err != nil {
			return Errf(time.Since(start), "%s", describeRequestError(err))
		}
		// The body is irrelevant; draining a little lets the connection be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()

		finalCode = resp.StatusCode
		codes = append(codes, fmt.Sprint(finalCode))
		location := resp.Header.Get("Location")
		if finalCode < 300 || finalCode >= 400 || location == "" {
			final = current
			break
		}
		if hops == maxHops {
			return Outcome{
				Success: false,
				Latency: time.Since(start),
				Message: fmt.Sprintf("%s → … more than %d redirects", strings.Join(codes, " → "), maxHops),
				Err:     errors.New("redirect limit reached"),
			}
		}

		base, err := url.Parse(current)
		if err != nil {
			return Errf(time.Since(start), "parse %q: %v", current, err)
		}
		ref, err := url.Parse(location)
		if err != nil {
			return Outcome{
				Success: false,
				Latency: time.Since(start),
				Message: fmt.Sprintf("HTTP %d sent an unusable Location %q", finalCode, truncate(location, 100)),
				Err:     errors.New("invalid redirect location"),
			}
		}
		current = base.ResolveReference(ref).String()
	}

	chain := strings.Join(codes, " → ")
	spec, err := parseStatusSpec(finalStatusSpec(c.Options))
	if err != nil {
		return Errf(time.Since(start), "invalid expected_status: %v", err)
	}
	if !spec.match(finalCode) {
		return Outcome{
			Success:    false,
			Latency:    time.Since(start),
			StatusCode: finalCode,
			Message:    fmt.Sprintf("%s · final %s · expected %s", chain, truncate(final, 100), spec.String()),
			Err:        fmt.Errorf("final status %d is not expected", finalCode),
		}
	}
	if want := c.Options.Str("expected_final", ""); want != "" && !strings.Contains(final, want) {
		return Outcome{
			Success:    false,
			Latency:    time.Since(start),
			StatusCode: finalCode,
			Message:    fmt.Sprintf("%s · final %s does not contain %q", chain, truncate(final, 100), want),
			Err:        errors.New("unexpected final URL"),
		}
	}

	ms := float64(time.Since(start).Microseconds()) / 1000
	return Outcome{
		Success:    true,
		Latency:    time.Since(start),
		StatusCode: finalCode,
		Message:    fmt.Sprintf("%s · final %s · %.0fms", chain, truncate(final, 100), ms),
	}
}
