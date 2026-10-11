package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cleanEnv removes every PROBEGLASS_* variable the loader reads. t.Setenv
// restores the previous value when the test ends.
func cleanEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"PROBEGLASS_CONFIG", "PROBEGLASS_LISTEN", "PROBEGLASS_DATA_DIR",
		"PROBEGLASS_DB", "PROBEGLASS_BASE_URL", "PROBEGLASS_LOG_LEVEL",
		"PROBEGLASS_LOG_FORMAT", "PROBEGLASS_ADMIN_PASSWORD",
		"PROBEGLASS_TRUST_PROXY", "PROBEGLASS_RETENTION_RAW",
		"PROBEGLASS_RETENTION_HOURLY",
	} {
		t.Setenv(key, "")
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probeglass.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	cleanEnv(t)
	cfg, err := Load("", Overrides{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != ":8080" {
		t.Errorf("listen = %q, want :8080", cfg.Listen)
	}
	if cfg.DBPath != filepath.Join("./data", "probeglass.db") {
		t.Errorf("db path = %q", cfg.DBPath)
	}
	if cfg.Retention.Raw.D() != 7*24*time.Hour {
		t.Errorf("raw retention = %s, want 168h", cfg.Retention.Raw.D())
	}
	if cfg.Retention.Hourly.D() != 90*24*time.Hour {
		t.Errorf("hourly retention = %s, want 2160h", cfg.Retention.Hourly.D())
	}
	if cfg.Path != "" {
		t.Errorf("config path = %q, want empty when no file was loaded", cfg.Path)
	}
}

func TestLoadPrecedence(t *testing.T) {
	cleanEnv(t)
	path := writeConfig(t, `
listen: ":9000"
db_path: /srv/file.sqlite
log_level: warn
base_url: "http://file.example:9000/"
retention:
  raw: 24h
  hourly: 720h
sensors:
  - name: from-file
    type: http
    target: https://example.org
    interval: 30s
channels:
  - name: from-file-alerts
    type: ntfy
    config:
      topic: homelab
`)
	t.Setenv("PROBEGLASS_LISTEN", ":9100")
	t.Setenv("PROBEGLASS_LOG_LEVEL", "error")

	cfg, err := Load(path, Overrides{Listen: ":9200"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// The flag beats the environment, the environment beats the file, and the
	// file beats the built-in default.
	if cfg.Listen != ":9200" {
		t.Errorf("listen = %q, want the flag value :9200", cfg.Listen)
	}
	if cfg.LogLevel != "error" {
		t.Errorf("log level = %q, want the environment value error", cfg.LogLevel)
	}
	if cfg.DBPath != "/srv/file.sqlite" {
		t.Errorf("db path = %q, want the file value", cfg.DBPath)
	}
	if cfg.LogFormat != "text" {
		t.Errorf("log format = %q, want the built-in default", cfg.LogFormat)
	}
	// A trailing slash on base_url would produce doubled slashes in alert links.
	if cfg.BaseURL != "http://file.example:9000" {
		t.Errorf("base url = %q, want the trailing slash trimmed", cfg.BaseURL)
	}
	if cfg.Retention.Raw.D() != 24*time.Hour {
		t.Errorf("raw retention = %s, want 24h from the file", cfg.Retention.Raw.D())
	}
	if len(cfg.Sensors) != 1 || cfg.Sensors[0].Name != "from-file" {
		t.Fatalf("sensors = %+v", cfg.Sensors)
	}
	if cfg.Sensors[0].Interval.D() != 30*time.Second {
		t.Errorf("interval = %s, want 30s", cfg.Sensors[0].Interval.D())
	}
	if len(cfg.Channels) != 1 || cfg.Channels[0].Type != "ntfy" {
		t.Fatalf("channels = %+v", cfg.Channels)
	}
}

func TestDurationAcceptsStringsAndSeconds(t *testing.T) {
	cleanEnv(t)
	path := writeConfig(t, `
sensors:
  - name: spelled-out
    type: http
    target: https://example.org
    interval: 90s
    timeout: 5s
  - name: bare-number
    type: http
    target: https://example.org
    interval: 45
    timeout: 3
`)
	cfg, err := Load(path, Overrides{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Sensors[0].Interval.D(); got != 90*time.Second {
		t.Errorf("spelled-out interval = %s, want 90s", got)
	}
	if got := cfg.Sensors[0].Timeout.D(); got != 5*time.Second {
		t.Errorf("spelled-out timeout = %s, want 5s", got)
	}
	if got := cfg.Sensors[1].Interval.D(); got != 45*time.Second {
		t.Errorf("bare-number interval = %s, want 45s", got)
	}
	if got := cfg.Sensors[1].Timeout.D(); got != 3*time.Second {
		t.Errorf("bare-number timeout = %s, want 3s", got)
	}
}

func TestParseDurationAcceptsDaysAndWeeks(t *testing.T) {
	cases := map[string]time.Duration{
		"90s":   90 * time.Second,
		"5m":    5 * time.Minute,
		"24h":   24 * time.Hour,
		"1d":    24 * time.Hour,
		"7d":    7 * 24 * time.Hour,
		"90d":   90 * 24 * time.Hour,
		"1.5d":  36 * time.Hour,
		"2w":    14 * 24 * time.Hour,
		" 7d  ": 7 * 24 * time.Hour,
	}
	for in, want := range cases {
		got, err := ParseDuration(in)
		if err != nil {
			t.Errorf("ParseDuration(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseDuration(%q) = %s, want %s", in, got, want)
		}
	}
	for _, bad := range []string{"", "soon", "7 days", "1y", "d"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q) accepted an invalid duration", bad)
		}
	}
}

func TestRetentionAcceptsDayNotationInTheFile(t *testing.T) {
	cleanEnv(t)
	path := writeConfig(t, "retention:\n  raw: 7d\n  hourly: 90d\n")
	cfg, err := Load(path, Overrides{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Retention.Raw.D() != 7*24*time.Hour {
		t.Errorf("raw retention = %s, want 168h", cfg.Retention.Raw.D())
	}
	if cfg.Retention.Hourly.D() != 90*24*time.Hour {
		t.Errorf("hourly retention = %s, want 2160h", cfg.Retention.Hourly.D())
	}
}

func TestRetentionFromEnvironment(t *testing.T) {
	cleanEnv(t)
	t.Setenv("PROBEGLASS_RETENTION_RAW", "48h")
	t.Setenv("PROBEGLASS_RETENTION_HOURLY", "8760h")

	cfg, err := Load("", Overrides{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Retention.Raw.D() != 48*time.Hour {
		t.Errorf("raw retention = %s, want 48h", cfg.Retention.Raw.D())
	}
	if cfg.Retention.Hourly.D() != 8760*time.Hour {
		t.Errorf("hourly retention = %s, want 8760h", cfg.Retention.Hourly.D())
	}
}

func TestMissingExplicitConfigIsAnError(t *testing.T) {
	cleanEnv(t)
	// A missing default file is tolerated...
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), Overrides{}); err != nil {
		t.Errorf("a missing default config should not be fatal: %v", err)
	}
	// ...but a requested one that is missing must stop startup.
	t.Setenv("PROBEGLASS_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	if _, err := Load("", Overrides{}); err == nil {
		t.Error("a missing PROBEGLASS_CONFIG file was ignored")
	}
}

func TestValidateRejectsBadConfiguration(t *testing.T) {
	cases := []struct {
		name   string
		config string
		want   string
	}{
		{"bad log level", "log_level: loud\n", "invalid log_level"},
		{"bad log format", "log_format: xml\n", "invalid log_format"},
		{"raw retention below an hour", "retention:\n  raw: 30m\n  hourly: 90d\n", "at least 1h"},
		{"hourly shorter than raw", "retention:\n  raw: 7d\n  hourly: 24h\n", "must not be shorter"},
		{"sensor without a name", "sensors:\n  - type: http\n    target: https://example.org\n", "name is required"},
		{"duplicate sensor names", "sensors:\n  - {name: web, type: http, target: 'https://example.org'}\n  - {name: web, type: http, target: 'https://other.example'}\n", "duplicate sensor name"},
		{"unknown sensor type", "sensors:\n  - {name: web, type: smoke, target: x}\n", "invalid type"},
		{"sensor without a target", "sensors:\n  - {name: web, type: http}\n", "target is required"},
		{"interval too short", "sensors:\n  - {name: web, type: http, target: 'https://example.org', interval: 2s}\n", "at least 5s"},
		{"interval too long", "sensors:\n  - {name: web, type: http, target: 'https://example.org', interval: 72h}\n", "must not exceed 24h"},
		{"retries out of range", "sensors:\n  - {name: web, type: http, target: 'https://example.org', retries: 0}\n", "retries"},
		{"channel without a name", "channels:\n  - type: ntfy\n", "name is required"},
		{"duplicate channel names", "channels:\n  - {name: a, type: ntfy}\n  - {name: a, type: ntfy}\n", "duplicate channel name"},
	}
	for _, tc := range cases {
		cleanEnv(t)
		path := writeConfig(t, tc.config)
		_, err := Load(path, Overrides{})
		if err == nil {
			t.Errorf("%s: an invalid configuration was accepted", tc.name)
			continue
		}
		if !contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func TestIsTruthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " y "} {
		if !IsTruthy(v) {
			t.Errorf("IsTruthy(%q) = false", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "maybe"} {
		if IsTruthy(v) {
			t.Errorf("IsTruthy(%q) = true", v)
		}
	}
}

func TestEnvInt(t *testing.T) {
	t.Setenv("PG_TEST_INT", "42")
	if got := EnvInt("PG_TEST_INT", 7); got != 42 {
		t.Errorf("EnvInt = %d, want 42", got)
	}
	t.Setenv("PG_TEST_INT", "not a number")
	if got := EnvInt("PG_TEST_INT", 7); got != 7 {
		t.Errorf("EnvInt of garbage = %d, want the default 7", got)
	}
	if got := EnvInt("PG_TEST_INT_UNSET", 7); got != 7 {
		t.Errorf("EnvInt of an unset variable = %d, want the default 7", got)
	}
}

// TestRenamedKeyIsRejected: the top-level list was called "checks" before the
// rename. A file that still says so must fail loudly, because the alternative —
// a strict decoder silently dropping the key, or a lenient one doing the same —
// is a homelab that looks configured and monitors nothing.
func TestRenamedKeyIsRejected(t *testing.T) {
	cleanEnv(t)
	path := writeConfig(t, `
checks:
  - name: web
    type: http
    target: https://example.org
`)
	_, err := Load(path, Overrides{})
	if err == nil {
		t.Fatal("a config using the old key checks: was accepted")
	}
	if !strings.Contains(err.Error(), "checks") || !strings.Contains(err.Error(), "sensors") {
		t.Errorf("error = %q, want it to name both the old key and the new one", err)
	}
}

// TestUnknownKeyIsRejected: before the rename the loader was lenient, so a
// misspelled key was dropped without a word. It is now a startup failure.
func TestUnknownKeyIsRejected(t *testing.T) {
	cleanEnv(t)
	path := writeConfig(t, `
lissten: ":9090"
`)
	if _, err := Load(path, Overrides{}); err == nil {
		t.Fatal("a config with an unknown key was accepted")
	}
}

// TestSensorsKeyStillWorks guards the guard: the rejection above must not have
// been written so broadly that the new spelling trips it too.
func TestSensorsKeyStillWorks(t *testing.T) {
	cleanEnv(t)
	path := writeConfig(t, `
sensors:
  - name: web
    type: http
    target: https://example.org
`)
	cfg, err := Load(path, Overrides{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Sensors) != 1 || cfg.Sensors[0].Name != "web" {
		t.Fatalf("sensors = %+v, want the one declared", cfg.Sensors)
	}
}

// TestAddressHelpers pins the two conversions the dashboard URL and the doctor
// probe both depend on: a wildcard bind is dialled on loopback but displayed as
// localhost, and a specific bind keeps its host either way.
func TestAddressHelpers(t *testing.T) {
	cases := []struct {
		listen, dial, display string
	}{
		{":8080", "127.0.0.1:8080", "localhost:8080"},
		{"0.0.0.0:9000", "127.0.0.1:9000", "localhost:9000"},
		{"[::]:8080", "127.0.0.1:8080", "localhost:8080"},
		{"192.168.1.5:8080", "192.168.1.5:8080", "192.168.1.5:8080"},
		{"not-an-address", "not-an-address", "not-an-address"},
	}
	for _, c := range cases {
		if got := DialAddr(c.listen); got != c.dial {
			t.Errorf("DialAddr(%q) = %q, want %q", c.listen, got, c.dial)
		}
		if got := DisplayAddr(c.listen); got != c.display {
			t.Errorf("DisplayAddr(%q) = %q, want %q", c.listen, got, c.display)
		}
	}
}
