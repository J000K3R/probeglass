package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/config"
	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// rendered is the report as the user sees it, which is what an assertion about
// the output has to look at. It doubles as the failure message when a finding is
// missing.
func rendered(r Report) string {
	var b bytes.Buffer
	r.Render(&b)
	return b.String()
}

// finding returns the single finding with a label. Every assertion here is about
// one label, so indexing into the slice would only make the tests brittle
// against a check being reordered or added.
func finding(t *testing.T, r Report, label string) Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.Label == label {
			return f
		}
	}
	t.Fatalf("no finding labelled %q in:\n%s", label, rendered(r))
	return Finding{}
}

// testConfig builds a configuration without reading the environment, so the
// tests do not depend on whatever PROBEGLASS_* variables the machine running
// them happens to export.
func testConfig(dir string) *config.Config {
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.DBPath = filepath.Join(dir, "probeglass.db")
	// Nothing listens on port 1, so the daemon check warns unless a test points
	// it at a server of its own.
	cfg.Listen = "127.0.0.1:1"
	return cfg
}

// healthServer answers /healthz the way the daemon does, so the daemon check can
// be exercised without starting one. It returns the host:port to hand to Options.
func healthServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","version":"test","uptime":"1s"}`)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// seed fills a database the way the daemon would, and closes it again.
func seed(t *testing.T, cfg *config.Config) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(cfg.DBPath, quiet())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	c := model.Sensor{Name: "gateway", Type: model.SensorHTTP, Target: "https://example.org", Enabled: true}
	store.NormaliseSensor(&c)
	if err := st.CreateSensor(ctx, &c); err != nil {
		t.Fatalf("create sensor: %v", err)
	}
	ch := model.Channel{
		Name:   "alerts",
		Type:   "webhook",
		Config: map[string]string{"url": "https://example.org/hook"},
	}
	store.NormaliseChannel(&ch)
	if err := st.CreateChannel(ctx, &ch); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if err := st.CreateUser(ctx, &model.User{
		Username:     "homelab",
		PasswordHash: "a-bcrypt-hash",
		Role:         model.RoleAdmin,
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
}

func dirEntries(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

// ---------------------------------------------------------------------------
// the healthy path
// ---------------------------------------------------------------------------

// TestHealthyInstallationReportsNoFailure is the baseline: a database the daemon
// would have written, a running daemon and an administrator account must produce
// nothing worse than warnings. If this test ever fails, doctor is crying wolf, and
// a diagnostic nobody believes is worth nothing.
func TestHealthyInstallationReportsNoFailure(t *testing.T) {
	cfg := testConfig(t.TempDir())
	seed(t, cfg)

	report := Run(context.Background(), Options{
		Config:     cfg,
		HealthAddr: healthServer(t),
		Timeout:    10 * time.Second,
	})

	if report.Failed() {
		t.Fatalf("a healthy installation reported a failure:\n%s", rendered(report))
	}
	// Every check that has something to say about a healthy installation. "icmp"
	// is absent on purpose: it stays silent when no ping sensor exists.
	for _, label := range []string{
		"build", "config", "data dir", "database",
		"integrity", "schema", "users", "contents", "daemon",
	} {
		if f := finding(t, report, label); f.Level != OK {
			t.Errorf("%s = %s, want ok (%s)", label, f.Level, f.Detail)
		}
	}
	if f := finding(t, report, "contents"); !strings.Contains(f.Detail, "1 sensor") {
		t.Errorf("contents = %q, want it to count the one sensor", f.Detail)
	}
	if f := finding(t, report, "users"); !strings.Contains(f.Detail, "1 account") {
		t.Errorf("users = %q, want it to count the one account", f.Detail)
	}
}

// ---------------------------------------------------------------------------
// the broken paths
// ---------------------------------------------------------------------------

// TestMissingDatabaseWarnsAndCreatesNothing pins the "changes nothing" rule for
// the most common fresh state. A first start races this command, so a missing
// file is a warning; and it must stay missing, because a database doctor created
// itself would be an empty one the daemon then trusts.
func TestMissingDatabaseWarnsAndCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)

	before := dirEntries(t, dir)
	report := Run(context.Background(), Options{
		Config:     cfg,
		HealthAddr: "127.0.0.1:1",
		Timeout:    5 * time.Second,
	})

	if report.Failed() {
		t.Fatalf("a fresh installation is not a failure:\n%s", rendered(report))
	}
	if f := finding(t, report, "database"); f.Level != Warn {
		t.Errorf("database = %s, want warn", f.Level)
	}
	if _, err := os.Stat(cfg.DBPath); !os.IsNotExist(err) {
		t.Errorf("doctor left a database behind (stat: %v)", err)
	}
	if after := dirEntries(t, dir); after != before {
		t.Errorf("the data directory changed: %q became %q", before, after)
	}
}

// TestBrokenConfigStillReports is the reason config.Resolve exists next to
// config.Load. A doctor built on Load would return nil for the config and be
// unable to name the data directory — precisely in the case it is run for.
func TestBrokenConfigStillReports(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	cfg.Path = filepath.Join(dir, "kaputt.yaml")

	report := Run(context.Background(), Options{
		Config: cfg,
		ConfigErr: errors.New(
			`parse config /data/probeglass.yaml: yaml: line 2: did not find expected '-' indicator`),
		HealthAddr: "127.0.0.1:1",
		Timeout:    5 * time.Second,
	})

	if !report.Failed() {
		t.Fatalf("an unparseable configuration was not a failure:\n%s", rendered(report))
	}
	f := finding(t, report, "config")
	if f.Level != Fail {
		t.Errorf("config = %s, want fail", f.Level)
	}
	if f.Hint == "" {
		t.Error("the config finding has no hint, so the reader is not told what to do")
	}
	// The whole point: the rest of the report is still there, and still names the
	// paths resolved from the flags and the environment.
	if got := finding(t, report, "data dir"); !strings.Contains(got.Detail, dir) {
		t.Errorf("data dir = %q, want it to name %s", got.Detail, dir)
	}
	if got := finding(t, report, "database"); !strings.Contains(got.Detail, cfg.DBPath) {
		t.Errorf("database = %q, want it to name %s", got.Detail, cfg.DBPath)
	}
}

// TestCorruptDatabaseFailsAndIsNotRepaired is the central promise of the whole
// package: doctor describes a damaged installation and leaves it exactly as it
// found it. The byte comparison is what proves it — the failure itself could
// come from any number of checks.
func TestCorruptDatabaseFailsAndIsNotRepaired(t *testing.T) {
	cfg := testConfig(t.TempDir())
	seed(t, cfg)

	// Overwrite the last page. It is guaranteed to be inside the file and inside
	// the database body, so SQLite cannot mistake it for a new file, and on a
	// database this size the last page holds the settings index — which means the
	// damage survives the open and is only caught by quick_check. That is the
	// path this test is here for; a page the header depends on would be caught
	// earlier, by the open itself.
	info, err := os.Stat(cfg.DBPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	f, err := os.OpenFile(cfg.DBPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open for corruption: %v", err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{0xAB}, 4096), info.Size()-4096); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	f.Close()

	before, err := os.ReadFile(cfg.DBPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	report := Run(context.Background(), Options{
		Config:     cfg,
		HealthAddr: "127.0.0.1:1",
		Timeout:    20 * time.Second,
	})
	if !report.Failed() {
		t.Fatalf("a corrupt database was reported healthy:\n%s", rendered(report))
	}
	if f := finding(t, report, "integrity"); f.Level != Fail {
		t.Errorf("integrity = %s, want fail (%s)", f.Level, f.Detail)
	}

	after, err := os.ReadFile(cfg.DBPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("doctor modified the database file; it must report, not repair")
	}
}

// TestICMPIsProbedOnlyWhenAPingSensorExists: on a host that runs no ping sensor
// the answer is noise, and noise is what makes people stop reading a report.
func TestICMPIsProbedOnlyWhenAPingSensorExists(t *testing.T) {
	cfg := testConfig(t.TempDir())
	seed(t, cfg)

	base := Options{Config: cfg, HealthAddr: "127.0.0.1:1", Timeout: 5 * time.Second}
	if containsLabel(Run(context.Background(), base), "icmp") {
		t.Error("icmp was reported although no ping sensor exists")
	}

	// Declare one in the file instead of the database: the check looks at both,
	// and a sensor that has not been applied yet is exactly the case worth
	// catching.
	paused := false
	cfg.Sensors = []config.SensorSpec{
		{Name: "router", Type: "ping", Target: "192.0.2.1", Enabled: &paused},
	}
	if containsLabel(Run(context.Background(), base), "icmp") {
		t.Error("icmp was reported for a ping sensor that is paused")
	}

	enabled := true
	cfg.Sensors[0].Enabled = &enabled
	if !containsLabel(Run(context.Background(), base), "icmp") {
		t.Error("icmp was not reported although an enabled ping sensor exists")
	}
}

func containsLabel(r Report, label string) bool {
	for _, f := range r.Findings {
		if f.Label == label {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// the report itself
// ---------------------------------------------------------------------------

func TestRenderShowsHintsAndSummary(t *testing.T) {
	r := Report{Findings: []Finding{
		{Level: OK, Label: "build", Detail: "probeglass test"},
		{Level: Warn, Label: "users", Detail: "the accounts table is empty",
			Hint: "open the dashboard to run the setup screen again"},
		{Level: Fail, Label: "integrity", Detail: "quick_check failed"},
	}}

	out := rendered(r)
	for _, want := range []string{
		"probeglass doctor",
		"ok    build",
		"warn  users",
		"hint: open the dashboard to run the setup screen again",
		"fail  integrity",
		"1 warning, 1 failure",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report is missing %q:\n%s", want, out)
		}
	}
}

func TestReportSemantics(t *testing.T) {
	var empty Report
	if empty.Failed() {
		t.Error("an empty report was reported as a failure")
	}
	if ok, warn, fail := empty.Counts(); ok != 0 || warn != 0 || fail != 0 {
		t.Errorf("empty counts = %d/%d/%d, want all zero", ok, warn, fail)
	}
	if got := rendered(empty); !strings.Contains(got, "0 warnings, 0 failures") {
		t.Errorf("an empty report summarises as %q", got)
	}

	// Warnings alone are not a failure. A fresh installation warns and works,
	// and doctor's exit code has to agree with that.
	warnOnly := Report{Findings: []Finding{{Level: Warn}, {Level: Warn}}}
	if warnOnly.Failed() {
		t.Error("warnings alone were reported as a failure")
	}
	if ok, warn, fail := warnOnly.Counts(); ok != 0 || warn != 2 || fail != 0 {
		t.Errorf("counts = %d/%d/%d, want 0/2/0", ok, warn, fail)
	}

	withFail := Report{Findings: []Finding{{Level: OK}, {Level: Warn}, {Level: Fail}}}
	if !withFail.Failed() {
		t.Error("a failure was not reported as such")
	}
	if ok, warn, fail := withFail.Counts(); ok != 1 || warn != 1 || fail != 1 {
		t.Errorf("counts = %d/%d/%d, want 1/1/1", ok, warn, fail)
	}
}

func TestLevelStrings(t *testing.T) {
	// Lowercase on purpose: a status word pasted into a chat must not be mistaken
	// for a log level.
	cases := map[Level]string{OK: "ok", Warn: "warn", Fail: "fail", Level(99): "?"}
	for level, want := range cases {
		if got := level.String(); got != want {
			t.Errorf("Level(%d) = %q, want %q", level, got, want)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:                      "0 B",
		512:                    "512 B",
		1024:                   "1 KiB",
		1536:                   "1.5 KiB",
		1536 * 1024:            "1.5 MiB",
		5 * 1024 * 1024:        "5 MiB",
		3 * 1024 * 1024 * 1024: "3 GiB",
	}
	for n, want := range cases {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestPlural(t *testing.T) {
	cases := map[int]string{0: "0 sensors", 1: "1 sensor", 2: "2 sensors"}
	for n, want := range cases {
		if got := plural(n, "sensor"); got != want {
			t.Errorf("plural(%d) = %q, want %q", n, got, want)
		}
	}
}
