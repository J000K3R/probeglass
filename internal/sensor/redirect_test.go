package sensor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/j000k3r/probeglass/internal/model"
)

// redirectServer answers /start with two redirects and /end with a page, and
// /loop with itself, which is the shape a misconfigured site takes.
func redirectServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/middle", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/middle", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/end", http.StatusFound)
	})
	mux.HandleFunc("/end", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/slow-end", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRedirectProberReportsTheChain(t *testing.T) {
	srv := redirectServer(t)
	got := RedirectProber{}.Run(greetingContext(t), validSensor(model.Sensor{
		Name: "redirect", Type: model.SensorRedirect, Target: srv.URL + "/start",
	}))

	if !got.Success {
		t.Fatalf("the chain was rejected: %s", got.Message)
	}
	for _, want := range []string{"301", "302", "200", "/end"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("message %q does not mention %q", got.Message, want)
		}
	}
	if got.StatusCode != 200 {
		t.Errorf("final status = %d, want 200", got.StatusCode)
	}
}

func TestRedirectProberChecksTheFinalURL(t *testing.T) {
	srv := redirectServer(t)
	got := RedirectProber{}.Run(greetingContext(t), validSensor(model.Sensor{
		Name: "redirect", Type: model.SensorRedirect, Target: srv.URL + "/start",
		Options: model.Options{"expected_final": "/somewhere-else"},
	}))

	if got.Success {
		t.Fatalf("a final URL without the expected text passed: %s", got.Message)
	}
}

func TestRedirectProberChecksTheFinalStatus(t *testing.T) {
	srv := redirectServer(t)
	got := RedirectProber{}.Run(greetingContext(t), validSensor(model.Sensor{
		Name: "redirect", Type: model.SensorRedirect, Target: srv.URL + "/slow-end",
	}))

	if got.Success {
		t.Fatalf("a 404 final page passed the default 200-299: %s", got.Message)
	}
	if got.StatusCode != http.StatusNotFound {
		t.Errorf("final status = %d, want 404", got.StatusCode)
	}
}

func TestRedirectProberStopsAtTheLimit(t *testing.T) {
	srv := redirectServer(t)
	got := RedirectProber{}.Run(greetingContext(t), validSensor(model.Sensor{
		Name: "redirect", Type: model.SensorRedirect, Target: srv.URL + "/loop",
		Options: model.Options{"max_hops": 3},
	}))

	if got.Success {
		t.Fatalf("a redirect loop passed: %s", got.Message)
	}
	if !strings.Contains(got.Message, "3 redirects") {
		t.Errorf("message %q does not name the limit", got.Message)
	}
}
