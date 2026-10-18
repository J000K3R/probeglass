// Command demogen writes the clickable demo that the website serves.
//
// The demo is not a likeness of the dashboard. It is the dashboard: the same
// files out of internal/web/assets, and the answers recorded from the same
// handler the daemon serves. Map, widgets, layouts, search, the tour and the
// language switch are all there because they are the same code, and nothing can
// fall out of step with the product short of this command being run again — which
// is what the checksum it writes is for.
//
// What it produces, in the directory named by -out:
//
//	index.html        the real shell, its five root-absolute paths made relative,
//	                  with a bar above it that says what the page is
//	assets/           copied verbatim from the embedded dashboard
//	fixtures.js       every answer the interface asked for, recorded
//	shim.js           replaces fetch, EventSource and the two storages
//	demo.css          the bar's styles
//	fingerprint.txt   what this demo is made of, so that CI can tell whether the
//	                  one being served is still the one this command would write
//
// The four files beyond assets/ sit at the root of the directory, because the
// directory is the demo: it is served as a subdirectory of somebody's website and
// its index.html is the page. There is no second level to keep them apart from the
// dashboard, any more than the daemon keeps them apart from it.
//
// The two browser APIs replaced are the only two the dashboard uses, and the
// storages are replaced because the demo must leave nothing behind in a
// visitor's browser. Settings changed in the demo are applied in memory and are
// gone on reload, which is what the bar promises.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/j000k3r/probeglass/internal/api"
	"github.com/j000k3r/probeglass/internal/config"
	"github.com/j000k3r/probeglass/internal/demo"
	"github.com/j000k3r/probeglass/internal/event"

	"github.com/j000k3r/probeglass/internal/notify"
	"github.com/j000k3r/probeglass/internal/scheduler"
	"github.com/j000k3r/probeglass/internal/store"
	"github.com/j000k3r/probeglass/internal/web"
)

// demoPassword is the account the recording signs in as. It never leaves this
// process: the login happens against a throwaway database in a temporary
// directory that is deleted when the command returns, and the session cookie it
// yields is not written anywhere.
const demoPassword = "demo-recording-only"

// ranges are the windows the detail window's range picker offers. They are the
// one set of parameters the interface chooses from a fixed list, so they can be
// enumerated and need no shim-side logic.
var ranges = []string{"1h", "24h", "7d", "30d", "90d"}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "demogen: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	out := flag.String("out", "", "directory to write the demo into (required)")
	days := flag.Int("days", 7, "how many days of history to invent")
	baseURL := flag.String("base-url", "", "the address the recording is served from")
	flag.Parse()
	if *out == "" {
		return fmt.Errorf("-out is required: it is where the demo is written")
	}

	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	tmp, err := os.MkdirTemp("", "probeglass-demogen")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	cfg, err := config.Load("", config.Overrides{
		DataDir: filepath.Join(tmp, "data"),
		BaseURL: *baseURL,
	})
	if err != nil {
		return err
	}
	// The account the recording signs in as. It exists only long enough to hold a
	// session for the requests below.
	cfg.AdminPassword = demoPassword

	st, err := store.Open(cfg.DBPath, logger)
	if err != nil {
		return err
	}
	defer st.Close()
	st.SetRetention(cfg.Retention.Raw.D(), cfg.Retention.Hourly.D())

	seeded, err := demo.Seed(ctx, st, *days)
	if err != nil {
		return err
	}
	fmt.Printf("seeded %d sensor(s), %d probe(s), %d site(s)\n",
		seeded.Sensors, seeded.Samples, seeded.Sites)

	// The demo is a folder of static files, and an uploaded icon is drawn with
	// <img src="/api/icons/7"> — a route only a server can answer. Recording the
	// drawing each uploaded example stands for keeps the demo from showing broken
	// pictures where the logos are supposed to be.
	if err := demo.RecordBuiltinIcons(ctx, st); err != nil {
		return err
	}

	// The scheduler is started and stopped, which is not the same as running it.
	//
	// Start restores, for every enabled sensor, the state a daemon would restore
	// after a restart: the status and the moment it began, read back out of the
	// history that was just seeded. Stop then cancels the probe loops before any of
	// them fires — each one waits a random three seconds before its first probe and
	// the cancellation is immediate — so nothing is dialled and the recording stays
	// exactly the dataset it was made from.
	//
	// Without this the whole dashboard would read "pending". A sensor's live status
	// is the scheduler's memory and not a column, so a summary taken from a scheduler
	// that had never been started would show a homelab watched for a week whose every
	// sensor has supposedly never been checked.
	broker := event.NewBroker()
	defer broker.Close()

	dispatcher := notify.NewDispatcher(st, logger, cfg.BaseURL)
	sched := scheduler.New(st, dispatcher, broker, logger)
	if err := sched.Start(ctx); err != nil {
		return err
	}
	sched.Stop()

	server, err := api.New(ctx, api.Options{
		Config:     cfg,
		Store:      st,
		Scheduler:  sched,
		Dispatcher: dispatcher,
		Broker:     broker,
		Logger:     logger,
		Assets:     web.Assets(),
		AssetETags: web.Fingerprints(),
	})
	if err != nil {
		return err
	}

	// A real server on a real socket, so the recording goes through the same
	// middleware, the same cookies and the same routing the daemon uses. Calling
	// the handlers directly would record answers nobody would ever receive.
	httpSrv := httptest.NewServer(server.Handler())
	defer httpSrv.Close()

	// The server's own transport, but a client of our own: the session cookie the
	// sign-in sets has to be sent back on every request below, and a client without
	// a jar drops it on the floor. The transport matters because httptest hands out
	// a real listener, and its client is the one that trusts it.
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	rec := &recorder{
		client: &http.Client{Transport: httpSrv.Client().Transport, Jar: jar},
		base:   httpSrv.URL,
		bodies: map[string]json.RawMessage{},
	}
	if err := rec.login(ctx); err != nil {
		return err
	}
	if err := rec.recordDashboard(ctx, st); err != nil {
		return err
	}
	if err := rec.recordSearch(ctx); err != nil {
		return err
	}
	if err := rec.scrub(); err != nil {
		return err
	}
	fmt.Printf("recorded %d response(s)\n", len(rec.bodies))

	return write(web.Assets(), *out, rec)
}

// scrub rewrites the three fields of the settings answer that describe the machine
// this command ran on rather than ProbeGlass.
//
// They are the throwaway database's path inside a temporary directory, the same path
// under another name, and how long this short-lived process has been up. A visitor
// who opens the settings screen in the demo would otherwise read
// /var/folders/sg/076j9…/T/probeglass-demogen1234/data — which is nobody's business,
// means nothing, and is a different string on every run, which would leave the demo's
// checksum moving for no reason.
//
// They are set to what the container image reports, which is also the address the
// documentation uses: /data, and a process that has been up for a few days. The uptime
// is written in the format the daemon writes it in, down to the unit.
func (r *recorder) scrub() error {
	const k = "GET /api/settings"
	raw, ok := r.bodies[k]
	if !ok {
		return fmt.Errorf("no %s recorded, so there is nothing to scrub", k)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("decode %s: %w", k, err)
	}
	runtime, _ := body["runtime"].(map[string]any)
	if runtime == nil {
		return fmt.Errorf("%s has no runtime block", k)
	}
	runtime["data_dir"] = "/data"
	runtime["database"] = "/data/probeglass.db"
	runtime["uptime"] = "102h15m3s"

	// Back through the encoder and not a textual patch of the bytes: the answers are
	// built by marshalling maps, and Go writes those in key order, so the result is
	// the same JSON with three values changed and nothing else moved.
	again, err := json.Marshal(body)
	if err != nil {
		return err
	}
	r.bodies[k] = again
	return nil
}

// recorder is one signed-in client against the throwaway installation.
type recorder struct {
	client *http.Client
	base   string
	// bodies maps a canonical request key to the exact bytes the server answered.
	// The bytes rather than a decoded value: the point of this exercise is to
	// serve the same thing twice, and a round trip through any type would be a
	// place for the two to differ.
	bodies map[string]json.RawMessage
	order  []string
}

// key names a request the way the shim will look it up: the method, the path, and
// the query parameters in a fixed order and a fixed escaping. Sorting the
// parameters is what lets the shim match a URL the interface assembled in a
// different order than the one recorded here.
func key(method, path string, params url.Values) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(method)
	b.WriteString(" ")
	b.WriteString(path)
	first := true
	for _, k := range keys {
		for _, v := range params[k] {
			if first {
				b.WriteString("?")
				first = false
			} else {
				b.WriteString("&")
			}
			b.WriteString(canonEscape(k))
			b.WriteString("=")
			b.WriteString(canonEscape(v))
		}
	}
	return b.String()
}

// canonEscape escapes a parameter the way JavaScript's encodeURIComponent does,
// because that is what the shim builds its half of the key with. Go's own
// url.QueryEscape differs in two ways that would matter here: it writes a space as
// "+", and it escapes ! ' ( ) * ~ where encodeURIComponent leaves them alone. A key
// built with the wrong one of the two would simply never be found, and a query with
// a space in it — a search for two words — is nothing unusual.
func canonEscape(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func (r *recorder) login(ctx context.Context) error {
	body, err := json.Marshal(map[string]string{
		"username": "admin",
		"password": demoPassword,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.base+"/api/login", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", r.base)
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sign in: %s", resp.Status)
	}
	return nil
}

// get records one request. Any answer that is not a 200 stops the run: a 500
// baked into the demo would be a broken panel on the website with nothing to say
// where it came from, and a 404 would mean this list has gone stale.
func (r *recorder) get(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", raw, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("get %s: %w", raw, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get %s: %s: %s", raw, resp.Status, truncate(string(body), 200))
	}
	k := key(http.MethodGet, u.Path, u.Query())
	r.bodies[k] = json.RawMessage(body)
	r.order = append(r.order, k)
	return nil
}

// recordDashboard walks the requests a fresh page load makes, in the order it
// makes them. Anything the interface reads belongs here; the list is the whole
// contract between this command and the shim, so a route added to the dashboard
// and forgotten here shows up as a demo that does not load rather than as one
// that quietly shows less.
func (r *recorder) recordDashboard(ctx context.Context, st *store.Store) error {
	fixed := []string{
		"/api/version",
		"/api/session",
		"/api/summary",
		"/api/sensors",
		"/api/sites",
		"/api/channels",
		"/api/channel-types",
		"/api/settings",
		"/api/examples",
		"/api/users",
		// The incident log's widest reading: no window, no filter. The shim filters
		// this list itself for the chips, which is exact — the filters are a
		// comparison against columns that are in the recorded rows.
		"/api/incidents?limit=200",
		// The groups and the icons. Both are read when somebody opens a picker or
		// the group editor, and both are one click away from a sensor form in the
		// demo — a route missing from this list is a demo that breaks under the
		// mouse rather than one that quietly shows less.
		"/api/groups",
		"/api/icons",
		// The maintenance windows the dataset arranges. The panel is two clicks
		// from the dashboard, and one of the two is in force, so its timestamps are
		// what the cards' badges are drawn from as well.
		"/api/maintenance",
	}
	for _, raw := range fixed {
		if err := r.get(ctx, raw); err != nil {
			return err
		}
	}

	sensors, err := st.ListSensors(ctx)
	if err != nil {
		return err
	}
	for _, s := range sensors {
		id := fmt.Sprintf("%d", s.ID)
		for _, raw := range []string{
			"/api/sensors/" + id,
			"/api/sensors/" + id + "/results?limit=40",
		} {
			if err := r.get(ctx, raw); err != nil {
				return err
			}
		}
		for _, rng := range ranges {
			for _, tail := range []string{"/series?range=", "/uptime?range="} {
				if err := r.get(ctx, "/api/sensors/"+id+tail+rng); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// recordSearch records the search box's answers for the words this dashboard is
// made of.
//
// Search is the one thing here that cannot be enumerated from a fixed list: the
// server matches any substring of six kinds of row, and the query is whatever
// somebody types. Mirroring that matching in the shim would be a second
// implementation of it, which is exactly the drift this whole exercise exists to
// avoid — so instead every word, and every prefix of a word of two characters or
// more, that appears in the answers already recorded is asked for here and the
// answers are kept.
//
// The corpus comes from those answers rather than from the database, and that is the
// rule: a word somebody can read on this page is a word the search answers for. A
// query that was never recorded gets an empty result, which is what the server says
// for a query that matches nothing at all.
func (r *recorder) recordSearch(ctx context.Context) error {
	// The endpoints that carry text. The series and uptime answers are left out on
	// purpose: they are numbers and timestamps, and walking them would ask for two
	// thousand queries made out of the characters of a date.
	var words []string
	for _, prefix := range []string{
		"GET /api/summary", "GET /api/sensors", "GET /api/sites", "GET /api/channels",
		"GET /api/channel-types", "GET /api/users", "GET /api/incidents",
	} {
		for k, raw := range r.bodies {
			if k == prefix || strings.HasPrefix(k, prefix+"?") || strings.HasPrefix(k, prefix+"/") {
				words = append(words, searchableWords(raw)...)
			}
		}
	}

	queries := map[string]bool{}
	for _, w := range words {
		for i := 2; i <= len(w); i++ {
			queries[w[:i]] = true
		}
	}

	list := make([]string, 0, len(queries))
	for q := range queries {
		list = append(list, q)
	}
	sort.Strings(list)
	for _, q := range list {
		if err := r.get(ctx, "/api/search?q="+canonEscape(q)); err != nil {
			return err
		}
	}
	fmt.Printf("recorded search responses for %d word(s)\n", len(list))
	return nil
}

// stamp matches the timestamps Go writes, the one kind of string in an answer that
// nobody can usefully type into a search box.
var stamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`)

// searchableWords collects the words out of one recorded answer.
//
// Every string in the JSON is looked at, at whatever depth it sits and whatever the
// field is called: the shape of these answers changes, and a walk that had to be
// told where the words are would be another list to keep in step with the handlers.
// Field names are skipped — "latency_ms" is not something anybody searches the
// dashboard for.
func searchableWords(raw json.RawMessage) []string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			if stamp.MatchString(t) {
				return
			}
			out = append(out, strings.FieldsFunc(strings.ToLower(t), func(r rune) bool {
				return !wordRune(r)
			})...)
		case []any:
			for _, item := range t {
				walk(item)
			}
		case map[string]any:
			for _, item := range t {
				walk(item)
			}
		}
	}
	walk(value)
	return out
}

// wordRune reports whether r belongs to a word, which is what the corpus above is
// divided into: a run of letters and digits. Everything else separates two words,
// and it has to: the search box matches substrings, so `192.168.1.10` has to be
// findable as `168` and a URL as the host inside it.
func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// write lays the demo out in dir. It is the second half of the command and the
// half that has to be right about paths: everything it writes is served from a
// subdirectory of somebody's website, where a path beginning with a slash leaves
// the demo entirely.
func write(assets fs.FS, dir string, rec *recorder) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		return err
	}

	// The dashboard's own files, byte for byte. This is the whole reason the demo
	// cannot drift: there is no copy of the dashboard here, there is the dashboard.
	if err := copyTree(assets, ".", filepath.Join(dir, "assets")); err != nil {
		return err
	}

	shell, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return err
	}
	page, err := rewriteShell(string(shell))
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "index.html"), []byte(page)); err != nil {
		return err
	}

	fixtures, err := json.MarshalIndent(map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"responses":    rec.bodies,
	}, "", "  ")
	if err != nil {
		return err
	}
	files := map[string]string{
		"fixtures.js": "window.__PROBEGLASS_FIXTURES__ = " + string(fixtures) + ";\n",
		"shim.js":     shimJS,
		"demo.css":    demoCSS,
	}
	for name, body := range files {
		if err := writeFile(filepath.Join(dir, name), []byte(body)); err != nil {
			return err
		}
	}

	stamp, err := fingerprint(assets, page, files, rec)
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "fingerprint.txt"), []byte(stamp+"\n")); err != nil {
		return err
	}
	fmt.Printf("wrote %s\nfingerprint %s\n", dir, stamp)
	return nil
}

// fingerprint is the value that makes "the demo is always the dashboard" a thing
// that can be checked rather than a thing that is claimed, and the website's CI
// compares it against the one committed beside the demo.
//
// It covers everything the demo serves that does not move on its own: the dashboard's
// files byte for byte, the shell this command rewrites out of them, the two files it
// writes itself, and the shape of every recorded answer.
//
// The shape and not the values, and this is the part worth being exact about. The
// history in the demo is a week that ends now — the recording is made at whatever
// moment somebody runs this command, and a latency, a sample count and a bucket
// boundary all move with it. A checksum over the values would therefore differ on
// every run, and a checksum with a short shelf life is not a check.
//
// What it does catch is every way "the demo is the product" stops being true: a file
// of the dashboard changed, the shell gained a path this rewrite does not know about,
// the shim or the bar changed, a route the interface calls is no longer recorded, a
// field in an answer was renamed or moved. Those change the number; the passage of
// time does not.
func fingerprint(assets fs.FS, page string, files map[string]string, rec *recorder) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(assets, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "asset %s %d\n", p, len(b))
		h.Write(b)
		return nil
	})
	if err != nil {
		return "", err
	}

	fmt.Fprintf(h, "shell %d\n", len(page))
	hashWrite(h, page)
	for _, name := range []string{"demo.css", "shim.js"} {
		fmt.Fprintf(h, "generated %s %d\n", name, len(files[name]))
		hashWrite(h, files[name])
	}

	keys := append([]string(nil), rec.order...)
	sort.Strings(keys)
	for _, k := range keys {
		var value any
		if err := json.Unmarshal(rec.bodies[k], &value); err != nil {
			return "", fmt.Errorf("shape of %s: %w", k, err)
		}
		shape := shapeOf(value)
		fmt.Fprintf(h, "answer %s %d\n", k, len(shape))
		hashWrite(h, shape)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// hashWrite feeds a string to the checksum being built. A hash's Write is
// documented never to return an error, and saying so once here keeps the three
// calls above from each carrying an error that cannot happen.
func hashWrite(h hash.Hash, s string) {
	_, _ = io.WriteString(h, s)
}

// shapeOf renders the structure of a decoded answer — its keys, its nesting and the
// type of every leaf — and drops every value. Two answers of the same shape are the
// same contract between the server and the interface; that is all this has to say,
// for the reason given on fingerprint.
//
// A list is represented by its first element, and an empty list by nothing: what is
// being recorded is that the field is still a list of that kind of thing, not how many
// rows happened to be in it.
func shapeOf(v any) string {
	var b strings.Builder
	writeShape(&b, v)
	return b.String()
}

func writeShape(b *strings.Builder, v any) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(b, "%q:", k)
			writeShape(b, t[k])
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		if len(t) > 0 {
			writeShape(b, t[0])
		}
		b.WriteByte(']')
	case string:
		b.WriteByte('s')
	case float64:
		b.WriteByte('n')
	case bool:
		b.WriteByte('b')
	case nil:
		b.WriteByte('z')
	default:
		b.WriteByte('?')
	}
}

// copyTree copies an fs.FS into a directory on disk.
func copyTree(src fs.FS, from, to string) error {
	return fs.WalkDir(src, from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.FromSlash(from), filepath.FromSlash(p))
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		return writeFile(dst, b)
	})
}

// writeFile writes one file of the demo.
//
// The mode is 0644 and the linter is told why rather than obeyed, because here the
// default it wants would break the thing being built: these are the files of a public
// website — the dashboard's own assets, its shell, three scripts and a checksum —
// written to be read by whoever opens the page and by whatever serves the directory.
// 0640 would make them unreadable to a web server running as a different user, and
// there is nothing in them to keep from anybody: the repository they are committed to
// is open.
func writeFile(path string, b []byte) error {
	return os.WriteFile(path, b, 0o644) //nolint:gosec // G306: served and committed, see above
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
