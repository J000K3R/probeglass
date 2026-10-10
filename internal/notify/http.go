package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// httpDoer is the small HTTP client every webhook-style notifier shares.
type httpDoer struct {
	client *http.Client
}

func newHTTPDoer(timeout time.Duration) *httpDoer {
	return &httpDoer{
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				ForceAttemptHTTP2:   true,
				MaxIdleConns:        16,
				IdleConnTimeout:     60 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
			},
		},
	}
}

// do sends a request and returns the response body. Any non-2xx answer becomes
// a classified error so the dispatcher knows whether retrying is worthwhile.
func (d *httpDoer) do(ctx context.Context, method, rawURL string, headers map[string]string, contentType string, body []byte) ([]byte, error) {
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		return nil, permanentError("invalid URL %q: %v", rawURL, err)
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, permanentError("build request: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", "Weatherglass")
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, retryableError("%s", describeHTTPError(err))
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return respBody, statusError(resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// doJSON marshals payload and posts it.
func (d *httpDoer) doJSON(ctx context.Context, method, rawURL string, headers map[string]string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, permanentError("encode payload: %v", err)
	}
	return d.do(ctx, method, rawURL, headers, "application/json", body)
}

func describeHTTPError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "DNS lookup failed for " + dnsErr.Name
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "connection timed out"
	}
	return err.Error()
}

// htmlEscape escapes the characters that matter inside Telegram HTML messages.
func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// joinTitleBody formats a title and body for plain-text destinations.
func joinTitleBody(title, body string) string {
	if strings.TrimSpace(title) == "" {
		return body
	}
	if strings.TrimSpace(body) == "" {
		return title
	}
	return title + "\n\n" + body
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func boolField(cfg map[string]string, key string) bool {
	switch strings.ToLower(FieldValue(cfg, key)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
