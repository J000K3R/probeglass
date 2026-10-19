// Package config loads ProbeGlass configuration from a YAML file and the
// environment. The environment always wins, which keeps container deployments
// simple while still allowing a sensored-in config file for the sensor
// definitions themselves.
//
// Precedence (low to high): built-in defaults, YAML file, environment.
//
// Durations accept Go syntax ("90s", "5m"), a bare number of seconds, and the
// day and week suffixes that retention settings are naturally written in
// ("7d", "2w").
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/j000k3r/probeglass/internal/model"
)

// Duration is a time.Duration that unmarshals from either a Go duration string
// ("90s", "5m") or a plain integer number of seconds.
type Duration time.Duration

// dayWeekRe matches durations written in days or weeks, which time.ParseDuration
// does not understand but which are the natural unit for retention settings.
var dayWeekRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)([dw])$`)

// ParseDuration accepts everything time.ParseDuration does, plus a bare number
// of seconds and the day and week suffixes ("90d", "2w", "1.5d").
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	if m := dayWeekRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", s, err)
		}
		hours := n * 24
		if m[2] == "w" {
			hours *= 7
		}
		return time.Duration(hours * float64(time.Hour)), nil
	}
	return time.ParseDuration(s)
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	// A bare number means seconds. The integer form is tested first because
	// yaml.v3 decodes any scalar into a string without complaining, which would
	// otherwise make the numeric form unreachable.
	if value.Tag == "!!int" {
		var n int64
		if err := value.Decode(&n); err == nil {
			*d = Duration(time.Duration(n) * time.Second)
			return nil
		}
	}
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like \"60s\" or a number of seconds")
	}
	parsed, err := ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// SensorSpec is one sensor as declared in the YAML config file.
type SensorSpec struct {
	Name     string   `yaml:"name"`
	Type     string   `yaml:"type"`
	Target   string   `yaml:"target"`
	Interval Duration `yaml:"interval"`
	Timeout  Duration `yaml:"timeout"`
	Retries  *int     `yaml:"retries"`
	Enabled  *bool    `yaml:"enabled"`
	Group    string   `yaml:"group"`
	// Site names a place the sensor belongs to, the same way group names a
	// heading. It is a name and not an id because a file has no way to know the
	// ids in someone's database, and one conversion has to serve both the file and
	// the API.
	Site string `yaml:"site"`
	// DependsOn names another sensor this one sits behind. A name for the reason Site
	// is one: a file has no way to know the ids in somebody's database.
	//
	// While that sensor is down, this one opens no incident of its own and its failed
	// checks do not count against its uptime. A power cut is one outage and not thirty.
	//
	// The file may declare them in any order. applyDeclarativeConfig makes two passes,
	// because a parent named further down the file would not exist yet on the first.
	DependsOn     string   `yaml:"depends_on"`
	Tags          []string `yaml:"tags"`
	ReminderEvery Duration `yaml:"reminder_every"`
	// EscalateAfter is how long an incident may stay open and unacknowledged
	// before EscalateChannels are told once. Empty or zero switches it off.
	EscalateAfter Duration `yaml:"escalate_after"`
	// EscalateChannels are alert channel names, resolved like Channels.
	EscalateChannels []string      `yaml:"escalate_channels"`
	Options          model.Options `yaml:"options"`
	Channels         []string      `yaml:"channels"`
	// Icon is the symbol drawn beside the sensor's name: `builtin:router` for one
	// of the drawings that ship with the program, `upload:7` for an image somebody
	// uploaded to this installation.
	//
	// It is here for the same reason every other field is: a sensor the file
	// declares is written from the file on every start, whole. A column the file
	// could not name would be cleared each time the daemon came up, and an icon set
	// in the dashboard on a file-owned sensor would quietly disappear overnight.
	Icon string `yaml:"icon"`
}

// SiteSpec is one site as declared in the YAML config file.
//
// A site declared here exists so that sensors can name it, and carries the
// coordinates so that a homelab described entirely in a file still draws on the
// map. Lat and Lon are pointers for the same reason as in the model: a site with
// no coordinates is an ordinary site, and 0/0 is a real place.
type SiteSpec struct {
	Name string   `yaml:"name"`
	Kind string   `yaml:"kind"`
	Lat  *float64 `yaml:"lat"`
	Lon  *float64 `yaml:"lon"`
	// Icon is the symbol for the place, in the same two forms a sensor's takes. It
	// is optional in the strong sense: a site with none keeps the pin its kind
	// gives it, which is what every site had before icons existed.
	Icon string `yaml:"icon"`
}

// ChannelSpec is one alert channel as declared in the YAML config file.
type ChannelSpec struct {
	Name    string            `yaml:"name"`
	Type    string            `yaml:"type"`
	Enabled *bool             `yaml:"enabled"`
	Config  map[string]string `yaml:"config"`
}

// ToSensor converts a declared sensor into the stored form.
//
// It lives here, next to the spec, so that the daemon and the diagnostic share
// one conversion: a configuration file that `probeglass doctor` accepts has to
// be the same one `serve` applies, and two copies of these fifteen lines would
// drift apart at the first new field.
//
// The caller normalises the result with store.NormaliseSensor.
func (s SensorSpec) ToSensor() model.Sensor {
	c := model.Sensor{
		Name:     s.Name,
		Type:     model.SensorType(s.Type),
		Target:   s.Target,
		Interval: int(s.Interval.D().Seconds()),
		Timeout:  int(s.Timeout.D().Seconds()),
		// A sensor declared in the file is on unless it says otherwise.
		Enabled: true,
		Group:   s.Group,
		Site:    s.Site,
		Parent:  strings.TrimSpace(s.DependsOn),
		Icon:    strings.TrimSpace(s.Icon),
		Tags:    s.Tags,
		Options: s.Options,
		Managed: true,
	}
	if s.Retries != nil {
		c.Retries = *s.Retries
	}
	if s.Enabled != nil {
		c.Enabled = *s.Enabled
	}
	if s.ReminderEvery.D() > 0 {
		c.ReminderEvery = int(s.ReminderEvery.D().Seconds())
	}
	if s.EscalateAfter.D() > 0 {
		c.EscalateAfter = int(s.EscalateAfter.D().Seconds())
	}
	return c
}

// ToSite converts a declared site into the stored form. Same reasoning as
// ToSensor: one conversion, used by the daemon and by the diagnostic.
//
// The caller normalises the result with store.NormaliseSite.
func (s SiteSpec) ToSite() model.Site {
	return model.Site{
		Name:    s.Name,
		Kind:    model.SiteKind(s.Kind),
		Lat:     s.Lat,
		Lon:     s.Lon,
		Icon:    strings.TrimSpace(s.Icon),
		Managed: true,
	}
}

// ToChannel converts a declared channel into the stored form. Same reasoning as
// ToSensor: one conversion, used by the daemon and by the diagnostic.
//
// The caller normalises the result with store.NormaliseChannel.
func (s ChannelSpec) ToChannel() model.Channel {
	ch := model.Channel{
		Name:    s.Name,
		Type:    s.Type,
		Config:  s.Config,
		Enabled: true,
		Managed: true,
	}
	if s.Enabled != nil {
		ch.Enabled = *s.Enabled
	}
	return ch
}

// Retention controls how long samples are kept at each resolution.
type Retention struct {
	// Raw is how long individual probe results are kept. Ranges shorter than
	// this are answered from raw data.
	Raw Duration `yaml:"raw"`
	// Hourly is how long hourly rollups are kept. Ranges longer than Raw but
	// shorter than Hourly are answered from hourly rollups.
	Hourly Duration `yaml:"hourly"`
	// Daily rollups are kept forever: they are tiny and they are what makes
	// multi-year uptime percentages possible.
}

// Config is the fully resolved runtime configuration.
type Config struct {
	Listen     string `yaml:"listen"`
	DataDir    string `yaml:"data_dir"`
	DBPath     string `yaml:"db_path"`
	BaseURL    string `yaml:"base_url"`
	LogLevel   string `yaml:"log_level"`
	LogFormat  string `yaml:"log_format"`
	TrustProxy bool   `yaml:"trust_proxy"`

	// AdminPassword, when set, bootstraps the dashboard password on a fresh
	// database. It is ignored once a password has been stored.
	AdminPassword string `yaml:"-"`

	Retention Retention     `yaml:"retention"`
	Sensors   []SensorSpec  `yaml:"sensors"`
	Sites     []SiteSpec    `yaml:"sites"`
	Channels  []ChannelSpec `yaml:"channels"`

	// Path is the config file that was actually loaded, empty when none was.
	Path string `yaml:"-"`
}

// Default returns the built-in defaults.
func Default() *Config {
	return &Config{
		Listen:    ":8080",
		DataDir:   "./data",
		LogLevel:  "info",
		LogFormat: "text",
		Retention: Retention{
			Raw:    Duration(7 * 24 * time.Hour),
			Hourly: Duration(90 * 24 * time.Hour),
		},
	}
}

// Load resolves the configuration. path may be empty, in which case
// $PROBEGLASS_CONFIG is consulted and then <data_dir>/probeglass.yaml is
// used if it exists.
// Overrides carries values from command line flags, which take precedence over
// both the configuration file and the environment. A zero value means "not set".
type Overrides struct {
	Listen    string
	DataDir   string
	DBPath    string
	BaseURL   string
	LogLevel  string
	LogFormat string
}

// Load builds the configuration. Precedence, lowest first: built-in defaults,
// the YAML file, PROBEGLASS_* environment variables, explicit overrides from
// command line flags.
//
// It is Resolve plus Validate: a configuration that would not start the daemon
// is an error here, which is what serve and seed want.
func Load(path string, overrides Overrides) (*Config, error) {
	cfg, err := Resolve(path, overrides)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Resolve builds the configuration the daemon would use, and always hands back
// a usable one.
//
// It exists for reporting commands. Load fails hard on a broken file, which is
// right for serve and wrong for anything whose job is to say what is broken: the
// reader of `probeglass doctor` most needs the paths exactly when the file is
// unparseable, and a nil config would leave the command unable to name the data
// directory it was asked about.
//
// The returned error describes the first problem found. The returned config is
// still filled in — defaults, environment and overrides are applied regardless —
// so its Path, DataDir and DBPath can be trusted and reported.
func Resolve(path string, overrides Overrides) (*Config, error) {
	cfg := Default()

	if path == "" {
		path = os.Getenv("PROBEGLASS_CONFIG")
	}
	if path == "" {
		if dd := os.Getenv("PROBEGLASS_DATA_DIR"); dd != "" {
			path = filepath.Join(dd, "probeglass.yaml")
		}
	}

	// The file is read first, but its failure does not stop the resolution: the
	// paths below come from the flags and the environment and are the answer
	// either way. cfg.Path stays empty when the file could not be read, which is
	// how the caller tells "no file" from "a file that is fine".
	var fileErr error
	if path != "" {
		if err := loadFile(cfg, path); err != nil {
			fileErr = err
			// Name the file anyway: the caller reports it, and "which file is
			// broken" is the first thing anyone asks.
			cfg.Path = path
		}
	}

	applyEnv(cfg)
	applyOverrides(cfg, overrides)
	cfg.finalise()
	return cfg, fileErr
}

func applyOverrides(cfg *Config, o Overrides) {
	if o.Listen != "" {
		cfg.Listen = o.Listen
	}
	if o.DataDir != "" {
		cfg.DataDir = o.DataDir
	}
	if o.DBPath != "" {
		cfg.DBPath = o.DBPath
	}
	if o.BaseURL != "" {
		cfg.BaseURL = o.BaseURL
	}
	if o.LogLevel != "" {
		cfg.LogLevel = o.LogLevel
	}
	if o.LogFormat != "" {
		cfg.LogFormat = o.LogFormat
	}
}

func loadFile(cfg *Config, path string) error {
	// A missing default config file is not an error; an explicitly requested
	// one that is missing is.
	//
	//nolint:gosec // G703: reading the configuration file the operator named is
	// the entire purpose of this function. The path comes from -config, from
	// PROBEGLASS_CONFIG, or from the data directory — never from a request.
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && os.Getenv("PROBEGLASS_CONFIG") == "" {
			return nil
		}
		return fmt.Errorf("read config %s: %w", path, err)
	}
	// The rename from "checks" to "sensors" is caught by hand rather than left
	// to the strict decoder below, because the decoder's own message ("field
	// checks not found in type config.Config") does not say what to write
	// instead — and a file that keeps the old key would otherwise silently
	// declare no sensors at all.
	//
	//nolint:misspell // "checks" is the old key, "sensors" the new one
	if err := rejectRenamedKeys(b); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}

	base := Default()
	*base = *cfg
	// KnownFields turns a typo into a startup failure. Without it a misspelled
	// key is dropped without a word, which is how a sensor that looks configured
	// ends up never running.
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(base); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	base.Path = path
	*cfg = *base
	return nil
}

// oldTopLevelKey is the name the top-level sensor list had before the rename.
// It is spelled out here, away from the sweep that renamed everything else, so
// that the guard cannot be quietly renamed along with the thing it guards.
const oldTopLevelKey = "checks"

// rejectRenamedKeys reports the pre-0.2 top-level key "checks" with the name it
// was renamed to. Parsing the document once as a bare node is the only way to
// name the offending key: the strict decoder only knows that some field was
// unknown, not which one the operator meant.
func rejectRenamedKeys(b []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		// Malformed YAML is reported by the real decode, with a line number.
		return nil
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil
	}
	top := root.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == oldTopLevelKey {
			return fmt.Errorf("the top-level key %q was renamed to %q: rename it "+
				"(the monitored objects are called sensors now)", oldTopLevelKey, "sensors")
		}
	}
	return nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("PROBEGLASS_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("PROBEGLASS_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("PROBEGLASS_DB"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("PROBEGLASS_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("PROBEGLASS_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("PROBEGLASS_LOG_FORMAT"); v != "" {
		cfg.LogFormat = v
	}
	if v := os.Getenv("PROBEGLASS_ADMIN_PASSWORD"); v != "" {
		cfg.AdminPassword = v
	}
	if v := os.Getenv("PROBEGLASS_TRUST_PROXY"); v != "" {
		cfg.TrustProxy = truthy(v)
	}
	if v := os.Getenv("PROBEGLASS_RETENTION_RAW"); v != "" {
		if d, err := ParseDuration(v); err == nil {
			cfg.Retention.Raw = Duration(d)
		}
	}
	if v := os.Getenv("PROBEGLASS_RETENTION_HOURLY"); v != "" {
		if d, err := ParseDuration(v); err == nil {
			cfg.Retention.Hourly = Duration(d)
		}
	}
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "t", "true", "yes", "y", "on":
		return true
	}
	return false
}

func (c *Config) finalise() {
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.DBPath == "" {
		c.DBPath = filepath.Join(c.DataDir, "probeglass.db")
	}
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	c.LogLevel = strings.ToLower(c.LogLevel)
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
}

// Validate checks the configuration for problems that should stop startup.
func (c *Config) Validate() error {
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid log_level %q (want debug, info, warn or error)", c.LogLevel)
	}
	switch c.LogFormat {
	case "", "text", "json":
	default:
		return fmt.Errorf("invalid log_format %q (want text or json)", c.LogFormat)
	}
	if c.Retention.Raw.D() < time.Hour {
		return fmt.Errorf("retention.raw must be at least 1h")
	}
	if c.Retention.Hourly.D() < c.Retention.Raw.D() {
		return fmt.Errorf("retention.hourly (%s) must not be shorter than retention.raw (%s)",
			c.Retention.Hourly.D(), c.Retention.Raw.D())
	}

	// A parent has to be a sensor this file declares or one the installation already
	// has, and the graph has to be a forest. Both are checked here rather than left to
	// the store, because a file is all one document: a typo in it is a typo in the
	// file, and saying so at startup names the line rather than the row.
	byName := map[string]string{}
	for _, ch := range c.Sensors {
		byName[ch.Name] = ch.DependsOn
	}
	for i, ch := range c.Sensors {
		if ch.DependsOn == "" {
			continue
		}
		if _, ok := byName[ch.DependsOn]; !ok {
			// Not in this file — the store will refuse it if the sensor is unknown
			// there either. Checked at apply time, where the answer is knowable.
			continue
		}
		seen := map[string]bool{ch.Name: true}
		for name, depth := ch.DependsOn, 0; name != ""; depth++ {
			if depth >= model.MaxParentDepth {
				return fmt.Errorf("sensors[%d]: the chain of parents is more than %d deep",
					i, model.MaxParentDepth)
			}
			if seen[name] {
				return fmt.Errorf("sensors[%d]: %q would end up in a circle of parents",
					i, ch.Name)
			}
			seen[name] = true
			name = byName[name]
		}
	}

	seen := map[string]bool{}
	for i, ch := range c.Sensors {
		if ch.Name == "" {
			return fmt.Errorf("sensors[%d]: name is required", i)
		}
		if seen[ch.Name] {
			return fmt.Errorf("sensors[%d]: duplicate sensor name %q", i, ch.Name)
		}
		seen[ch.Name] = true
		if !model.SensorType(ch.Type).Valid() {
			return fmt.Errorf("sensor %q: invalid type %q", ch.Name, ch.Type)
		}
		if ch.Target == "" {
			return fmt.Errorf("sensor %q: target is required", ch.Name)
		}
		if ch.Interval.D() != 0 && ch.Interval.D() < 5*time.Second {
			return fmt.Errorf("sensor %q: interval must be at least 5s", ch.Name)
		}
		if ch.Interval.D() > 24*time.Hour {
			return fmt.Errorf("sensor %q: interval must not exceed 24h", ch.Name)
		}
		if ch.Timeout.D() > 10*time.Minute {
			return fmt.Errorf("sensor %q: timeout must not exceed 10m", ch.Name)
		}
		if ch.Retries != nil && (*ch.Retries < 1 || *ch.Retries > 100) {
			return fmt.Errorf("sensor %q: retries must be between 1 and 100", ch.Name)
		}
	}
	seenCh := map[string]bool{}
	for i, ch := range c.Channels {
		if ch.Name == "" {
			return fmt.Errorf("channels[%d]: name is required", i)
		}
		if seenCh[ch.Name] {
			return fmt.Errorf("channels[%d]: duplicate channel name %q", i, ch.Name)
		}
		seenCh[ch.Name] = true
	}
	return nil
}

// IsTruthy exposes the truthy helper for other packages.
func IsTruthy(v string) bool { return truthy(v) }

// EnvInt reads an integer environment variable, returning def when unset or
// malformed.
func EnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// DisplayAddr turns a configured listen address into one a person can open in a
// browser: a wildcard host becomes "localhost", so ":8080" is reported as
// "localhost:8080" rather than "http://:8080".
func DisplayAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}

// DialAddr turns a configured listen address into one this process can dial.
//
// A wildcard bind address is not dialable, and the difference is easy to get
// wrong once and then debug for an hour. Everything else keeps its host, so a
// daemon bound to a specific interface is reached where it actually is rather
// than on loopback.
//
// It lives here rather than next to a caller because the dashboard, the
// doctor subcommand and the API's own diagnostic endpoint all probe the same
// place, and a second copy of these lines is how they would stop doing that.
func DialAddr(listen string) string {
	if host, port, err := net.SplitHostPort(listen); err == nil {
		if host == "" || host == "0.0.0.0" || host == "::" {
			return net.JoinHostPort("127.0.0.1", port)
		}
	}
	return DisplayAddr(listen)
}
