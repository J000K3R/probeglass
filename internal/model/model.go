// Package model defines the core domain types that are shared between the
// store, the probe engines, the scheduler and the HTTP API.
//
// Everything in here is deliberately dependency-free so that any layer can
// import it without creating an import cycle.
package model

import (
	"slices"
	"strings"
	"time"
)

// These are stamped in at build time with
// -ldflags "-X github.com/j000k3r/probeglass/internal/model.Version=X.Y.Z".
// They are what "probeglass version" and the About panel report, and they
// exist so a bug report can name the exact revision that produced it.
//
// The version carries no leading `v`, because the tag is the only thing that
// does. A build that stamps nothing — a plain `go build` — reports the string
// below instead, which is not a release.
var (
	Version   = "dev"
	Commit    = ""
	BuildDate = ""

	// BuildChannel is where this build came from, and is set by whichever workflow made
	// it. It is not the alert Channel above, which is a place notifications go:
	//
	//	release   a `v*` tag was pushed, and this is a published version
	//	dev       the newest build of main, published as `:dev` and not a release
	//	local     built from a working tree by `make build`, or by hand
	//
	// It exists because a container cannot see the tag it was pulled by. Two containers
	// on the same machine, one on `latest` and one on `dev`, are indistinguishable from
	// the inside — same version, same commit, same date — and the first question about
	// a feature that is not on screen is which of the two is being looked at.
	BuildChannel = "local"
)

// BuildChannelNote says in one line what the channel means, for the places that have
// room for a sentence and no room for a table. The dashboard reads the channel itself
// and writes its own words from its catalogue; this is for the terminal, which has none.
func BuildChannelNote() string {
	switch BuildChannel {
	case "release":
		return "a published version"
	case "dev":
		return "a build of main, newer than the last release and not a release itself"
	default:
		return "built from a working tree, not a release"
	}
}

// BuildInfo renders the version and, when the build stamped them in, the commit
// and build date.
func BuildInfo() string {
	out := Version
	if Commit != "" && Commit != "unknown" {
		commit := Commit
		if len(commit) > 12 {
			commit = commit[:12]
		}
		out += " (" + commit + ")"
	}
	if BuildDate != "" && BuildDate != "unknown" {
		out += " built " + BuildDate
	}
	return out
}

// SensorType identifies the kind of probe a sensor performs.
type SensorType string

const (
	SensorHTTP   SensorType = "http"
	SensorTCP    SensorType = "tcp"
	SensorPing   SensorType = "ping"
	SensorDNS    SensorType = "dns"
	SensorTLS    SensorType = "tls"
	SensorDocker SensorType = "docker"
	SensorSNMP   SensorType = "snmp"
)

// SensorTypes lists every supported sensor type. Valid uses it, and a test walks
// it to check that each one has a prober behind it; the dashboard keeps its own
// list, because it needs a label and a set of form fields for each type as well.
var SensorTypes = []SensorType{
	SensorHTTP, SensorTCP, SensorPing, SensorDNS, SensorTLS, SensorDocker, SensorSNMP,
}

// Valid reports whether t is a known sensor type.
func (t SensorType) Valid() bool {
	for _, c := range SensorTypes {
		if c == t {
			return true
		}
	}
	return false
}

// Status is the aggregated health of a sensor as presented to the user.
type Status string

const (
	// StatusUnknown means the sensor has never produced a result yet. It is the
	// answer to "is it up?" that is not a lie: nothing has been heard from it.
	StatusUnknown Status = "unknown"
	StatusUp      Status = "up"
	StatusDown    Status = "down"
	// StatusWarn marks a sensor whose last probe failed but whose failure streak
	// has not reached the configured retries yet — a blip, not an outage. It opens
	// no incident and sends no alert; the sensor is on its way down or back up, and
	// this is what it looks like while that is undecided.
	StatusWarn Status = "warn"
	// StatusPaused marks sensors that are disabled in the configuration.
	StatusPaused Status = "paused"
)

// Sensor is a single monitored target.
type Sensor struct {
	ID       int64      `json:"id"`
	Name     string     `json:"name"`
	Type     SensorType `json:"type"`
	Target   string     `json:"target"`
	Interval int        `json:"interval"` // seconds between probes
	Timeout  int        `json:"timeout"`  // seconds per probe
	// Retries is the number of consecutive failed probes required before the
	// sensor flips to DOWN. 1 means "alert on the very first failure".
	Retries int    `json:"retries"`
	Enabled bool   `json:"enabled"`
	Group   string `json:"group"`
	// Site names a row in the sites table: where this sensor physically is, the
	// homelab or a hoster. It is the site's name and not its row id on purpose —
	// a configuration file can name a place but can never know a database id, and
	// one conversion function has to serve both the file and the API. Empty means
	// "no site", which is a perfectly ordinary state: grouping and the tree work
	// without one, only the map has nothing to place.
	Site     string   `json:"site"`
	Tags     []string `json:"tags"`
	Options  Options  `json:"options"`
	Channels []int64  `json:"channel_ids"`
	// ReminderEvery re-sends the DOWN alert every N seconds while the sensor
	// stays down. 0 disables reminders (state transitions only).
	ReminderEvery int  `json:"reminder_every"`
	SortOrder     int  `json:"sort_order"`
	Managed       bool `json:"managed"` // declared in the config file, read-only in the UI
	// Origin records where a sensor came from when nobody typed it in by hand:
	// OriginExample for what the setup screen and `seed --demo` leave behind, and
	// empty for everything else. Without it, "remove all the example data" would
	// have to guess from the group name and a "(example)" suffix, and both of
	// those are things an operator is free to change.
	Origin    string    `json:"origin,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// OriginExample marks a sensor as invented rather than configured. It is the
// value of Sensor.Origin for the example sensors, and the only one the bulk
// delete in the settings screen will touch.
const OriginExample = "example"

// SiteKind is where a place physically is. The distinction is the point of the
// whole feature: a homelab box and a rented server fail for different reasons and
// are looked at by different people, and on a map they are not the same pin.
type SiteKind string

const (
	// SiteLocal is a place in the home or the office: the homelab.
	SiteLocal SiteKind = "local"
	// SiteExternal is somewhere a provider runs it — a hoster, a cloud, a VPS.
	SiteExternal SiteKind = "external"
)

// SiteKinds lists every kind, in the order the form should offer them.
var SiteKinds = []SiteKind{SiteLocal, SiteExternal}

// Valid reports whether k is a known site kind.
func (k SiteKind) Valid() bool {
	for _, c := range SiteKinds {
		if c == k {
			return true
		}
	}
	return false
}

// Site is a named place that sensors belong to.
//
// Everything about it is optional in effect: a sensor without a site behaves
// exactly as it did before sites existed, and a site without coordinates is still
// a working grouping — it simply has nothing to draw on the map. Lat and Lon are
// pointers because 0/0 is a real coordinate in the Gulf of Guinea, and a zero
// value therefore cannot be made to mean "not placed".
type Site struct {
	Name      string   `json:"name"`
	Kind      SiteKind `json:"kind"`
	Lat       *float64 `json:"lat,omitempty"`
	Lon       *float64 `json:"lon,omitempty"`
	SortOrder int      `json:"sort_order"`
	Managed   bool     `json:"managed"` // declared in the config file, read-only in the UI
	// Origin records that the site was invented rather than made by hand. It is
	// the same marker Sensor carries, and it is here for the same reason: the
	// example sensors need somewhere to live, and "remove the example data" has to
	// be able to tell a place it created from a place the operator made. Without
	// it that button leaves the invented sites behind as empty rows nobody can
	// account for.
	Origin    string    `json:"origin,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// SensorCount is how many sensors are placed here. It is not a column — it is
	// counted when the site is read, for the headings that say how much is behind
	// a place and for the warning before deleting one that is still in use.
	SensorCount int `json:"sensor_count"`
}

// Placed reports whether the site has coordinates and can be drawn on the map.
func (s Site) Placed() bool { return s.Lat != nil && s.Lon != nil }

// WindowKind is how a maintenance window repeats — or that it does not.
type WindowKind string

const (
	// WindowOnce is one stretch of wall-clock time with a beginning and an end.
	// It is what "I am swapping the disk on Saturday" needs.
	WindowOnce WindowKind = "once"
	// WindowWeekly is a time of day on chosen weekdays, repeating until it is
	// deleted. It is what the nightly snapshot and the Sunday backup need.
	WindowWeekly WindowKind = "weekly"
)

// WindowKinds lists every kind, in the order the form should offer them.
var WindowKinds = []WindowKind{WindowOnce, WindowWeekly}

// Valid reports whether k is a known window kind.
func (k WindowKind) Valid() bool {
	for _, c := range WindowKinds {
		if c == k {
			return true
		}
	}
	return false
}

// MaintenanceWindow is a period during which a sensor is expected to be down.
//
// What it is for is what does not happen. A probe that fails inside the window
// opens no incident and sends no alert, and the samples it records in there are
// counted out of the uptime figure — so a nightly backup stops being a nightly
// outage on the dashboard. The probe itself keeps running: a window is not a
// pause, and knowing whether the box came back early is worth the requests.
//
// It applies to one sensor or to a whole group, and exactly one of the two is set.
// A group is a string on the sensors rather than a table of its own, so a window
// naming a group no sensor carries matches nothing at all. That is the right answer
// to a typo: it silences nothing rather than silencing the wrong thing.
type MaintenanceWindow struct {
	ID int64 `json:"id"`

	// SensorID names one sensor. Zero means the window is for the whole of Group.
	SensorID int64  `json:"sensor_id,omitempty"`
	Group    string `json:"group,omitempty"`

	Kind WindowKind `json:"kind"`

	// StartsAt and EndsAt bound a Kind == WindowOnce window, and are nil on a
	// weekly one.
	//
	// Pointers rather than plain times for the reason Site.Lat is a pointer: a
	// zero time.Time is a real instant in the year 1, and once it is in a column
	// there is no telling it from "unset".
	StartsAt *time.Time `json:"starts_at,omitempty"`
	EndsAt   *time.Time `json:"ends_at,omitempty"`

	// Weekdays, StartMin and EndMin describe a Kind == WindowWeekly window: which
	// days, and the time of day as minutes past midnight.
	//
	// Minutes rather than a time.Time, because this is a time on a clock and not
	// an instant — storing today's date beside "03:00" would make the window
	// expire. EndMin may be at or below StartMin, which is a window that runs past
	// midnight; see Covers.
	Weekdays []int `json:"weekdays,omitempty"`
	StartMin int   `json:"start_min,omitempty"`
	EndMin   int   `json:"end_min,omitempty"`

	// Timezone is the IANA name that the weekly boundaries are read in. It exists
	// because everything else in this program is UTC and a person is not: "Sunday
	// at three" means three on their wall, twice a year and all.
	//
	// It is the only place in ProbeGlass that a zone other than UTC appears, which
	// is why it is stored per window rather than as a setting of the installation:
	// the server has no opinion about where its operator lives, and the dashboard
	// fills this in from the browser when a window is created.
	Timezone string `json:"timezone"`

	Note      string    `json:"note,omitempty"`
	Enabled   bool      `json:"enabled"`
	Managed   bool      `json:"managed"` // declared in the config file, read-only in the UI
	Origin    string    `json:"origin,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Covers reports whether this window is in force for a sensor at an instant.
//
// This is the one question anything asks a window, and it is asked from two places
// that must not be able to disagree: the scheduler, deciding whether to keep quiet
// about a failure, and the store, marking the sample that failure produced. Both
// come through here, so "the alert was suppressed" and "the sample was counted out"
// are the same answer to the same question rather than two answers that can drift.
//
// The sensor rather than its id, because a group window has to compare the group,
// and passing an id would leave the caller to look the sensor up and get it right.
func (w MaintenanceWindow) Covers(s Sensor, at time.Time) bool {
	if !w.Enabled {
		return false
	}
	if w.SensorID != 0 {
		if w.SensorID != s.ID {
			return false
		}
	} else if w.Group == "" || w.Group != s.Group {
		return false
	}

	switch w.Kind {
	case WindowOnce:
		if w.StartsAt == nil || w.EndsAt == nil {
			return false
		}
		return !at.Before(*w.StartsAt) && at.Before(*w.EndsAt)

	case WindowWeekly:
		loc, err := time.LoadLocation(w.Timezone)
		if err != nil {
			// A zone the embedded tzdata does not know, which after validation
			// should not happen. Refusing the window is the safe direction: the
			// other one is a window that covers the whole installation forever,
			// and an installation that has silently stopped alerting is worse
			// than one whose Sunday backup still reports itself.
			return false
		}
		local := at.In(loc)
		minutes := local.Hour()*60 + local.Minute()

		if w.EndMin > w.StartMin {
			return slices.Contains(w.Weekdays, int(local.Weekday())) &&
				minutes >= w.StartMin && minutes < w.EndMin
		}

		// The window runs past midnight. Its evening half belongs to a day that
		// was named; its morning half belongs to the day before, which is the one
		// that was named — "Sunday 23:00 to 01:00" means most of Sunday night, and
		// Monday morning is the tail of it.
		if minutes >= w.StartMin {
			return slices.Contains(w.Weekdays, int(local.Weekday()))
		}
		if minutes < w.EndMin {
			previous := (int(local.Weekday()) + 6) % 7
			return slices.Contains(w.Weekdays, previous)
		}
		return false
	}
	return false
}

// Options carries sensor-type specific settings. It is stored as a JSON object
// and validated by the individual prober implementations.
type Options map[string]any

// Str returns the option as a string, or def when absent.
func (o Options) Str(key, def string) string {
	if o == nil {
		return def
	}
	v, ok := o[key]
	if !ok || v == nil {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

// Int returns the option as an int, or def when absent or malformed.
func (o Options) Int(key string, def int) int {
	if o == nil {
		return def
	}
	switch v := o[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return def
}

// Bool returns the option as a bool, or def when absent or malformed.
func (o Options) Bool(key string, def bool) bool {
	if o == nil {
		return def
	}
	switch v := o[key].(type) {
	case bool:
		return v
	}
	return def
}

// StrMap returns a nested string map option, e.g. custom request headers.
func (o Options) StrMap(key string) map[string]string {
	out := map[string]string{}
	if o == nil {
		return out
	}
	m, ok := o[key].(map[string]any)
	if !ok {
		return out
	}
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// Result is a single probe outcome.
type Result struct {
	ID         int64     `json:"id"`
	SensorID   int64     `json:"sensor_id"`
	TS         time.Time `json:"ts"`
	Success    bool      `json:"success"`
	LatencyMS  float64   `json:"latency_ms"`
	StatusCode int       `json:"status_code,omitempty"`
	Message    string    `json:"message,omitempty"`

	// Maint marks a probe that landed inside a maintenance window, and is what
	// keeps it out of the uptime figure. It is recorded rather than the probe
	// being skipped, so the chart still shows that the box really was away —
	// "expected" is a fact about the outage, not about the measurement.
	Maint bool `json:"maint,omitempty"`
}

// Incident is one continuous period during which a sensor was down.
type Incident struct {
	ID         int64      `json:"id"`
	SensorID   int64      `json:"sensor_id"`
	StartedAt  time.Time  `json:"started_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	Cause      string     `json:"cause,omitempty"`

	// AckedAt is when a person acknowledged this outage — "I know, stop
	// reminding me" — and AckedBy is who, when the acknowledgement came from an
	// API key rather than the dashboard. Acknowledging is not the same as
	// pausing: the sensor keeps being probed and an unresolved outage stays
	// visible; only the repeat alerts fall silent until the sensor recovers.
	//
	// It hangs off the outage rather than the sensor on purpose. A sensor-level
	// flag could not say which outage was meant, and it would keep suppressing
	// reminders for the next outage too, long after the note stopped meaning
	// anything.
	AckedAt *time.Time `json:"acked_at,omitempty"`
	AckedBy string     `json:"acked_by,omitempty"`
	AckNote string     `json:"ack_note,omitempty"`
}

// Ongoing reports whether the incident has not been resolved yet.
func (i Incident) Ongoing() bool { return i.ResolvedAt == nil }

// Acknowledged reports whether someone acknowledged the outage and reminders are
// therefore suppressed.
func (i Incident) Acknowledged() bool { return i.AckedAt != nil }

// Duration returns the length of the incident. Ongoing incidents are measured
// against now.
func (i Incident) Duration(now time.Time) time.Duration {
	end := now
	if i.ResolvedAt != nil {
		end = *i.ResolvedAt
	}
	return end.Sub(i.StartedAt)
}

// Channel is a configured alert destination.
type Channel struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Config    map[string]string `json:"config"`
	Enabled   bool              `json:"enabled"`
	Managed   bool              `json:"managed"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// Sample is one aggregated time bucket, produced either from raw results or
// from a rollup table.
type Sample struct {
	TS       time.Time `json:"ts"`
	OK       int       `json:"ok"`
	Total    int       `json:"total"`
	LatencyS float64   `json:"-"`
	LatMin   float64   `json:"-"`
	LatMax   float64   `json:"-"`

	// Maint and MaintOK are the probes in this sample's bucket that landed inside
	// a maintenance window, and how many of those succeeded. OK and Total are
	// carried as they were recorded; the exclusion happens where the figure is
	// computed, so that a caller which wants to see the raw truth still can —
	// RecentResults and the tooltip on a bucket both do.
	Maint   int `json:"maint,omitempty"`
	MaintOK int `json:"maint_ok,omitempty"`
}

// Counted returns the sample with the maintenance probes taken out of both sides,
// which is what a figure is computed from.
func (s Sample) Counted() Sample {
	s.Total -= s.Maint
	s.OK -= s.MaintOK
	if s.Total < 0 {
		s.Total = 0
	}
	if s.OK < 0 {
		s.OK = 0
	}
	return s
}

// LatencyAvg returns the mean latency of the bucket in milliseconds.
func (s Sample) LatencyAvg() float64 {
	if s.OK == 0 {
		return 0
	}
	return s.LatencyS / float64(s.OK)
}

// SeriesPoint is a bucketed data point as returned by the API.
// SeriesPoint is one bar of a sensor's chart. Total and OK are the probes that
// were counted; Maint is how many were left out because they fell inside a
// maintenance window, so that a bar which is entirely maintenance can be drawn as
// maintenance rather than as a gap.
type SeriesPoint struct {
	TS       time.Time `json:"ts"`
	AvgMS    float64   `json:"avg_ms"`
	MinMS    float64   `json:"min_ms"`
	MaxMS    float64   `json:"max_ms"`
	OK       int       `json:"ok"`
	Total    int       `json:"total"`
	Maint    int       `json:"maint,omitempty"`
	UptimePC float64   `json:"uptime_pct"`
}

// Range names a time window used for history queries.
type Range string

const (
	Range1h  Range = "1h"
	Range24h Range = "24h"
	Range7d  Range = "7d"
	Range30d Range = "30d"
	Range90d Range = "90d"
	Range1y  Range = "1y"
	RangeAll Range = "all"
)

// Duration returns the wall-clock length of the range, plus a bool that is
// false for RangeAll (which has no fixed start).
func (r Range) Duration() (time.Duration, bool) {
	switch r {
	case Range1h:
		return time.Hour, true
	case Range24h:
		return 24 * time.Hour, true
	case Range7d:
		return 7 * 24 * time.Hour, true
	case Range30d:
		return 30 * 24 * time.Hour, true
	case Range90d:
		return 90 * 24 * time.Hour, true
	case Range1y:
		return 365 * 24 * time.Hour, true
	case RangeAll:
		return 0, false
	}
	return 24 * time.Hour, true
}

// Valid reports whether r is a known range.
func (r Range) Valid() bool {
	_, ok := map[Range]bool{
		Range1h: true, Range24h: true, Range7d: true, Range30d: true,
		Range90d: true, Range1y: true, RangeAll: true,
	}[r]
	return ok
}

// StatusView is the live health snapshot of a sensor, joining the stored
// configuration with the runtime state and the computed uptime window. This is
// what the dashboard renders as a card.
type StatusView struct {
	Sensor
	Status       Status     `json:"status"`
	Since        time.Time  `json:"since"`
	LastSensor   *time.Time `json:"last_sensor,omitempty"`
	LatencyMS    float64    `json:"latency_ms"`
	AvgLatencyMS float64    `json:"avg_latency_ms"`
	StatusCode   int        `json:"status_code,omitempty"`
	Message      string     `json:"message,omitempty"`
	FailStreak   int        `json:"fail_streak"`
	Uptime24h    float64    `json:"uptime_24h"`
	Uptime7d     float64    `json:"uptime_7d"`
	Uptime30d    float64    `json:"uptime_30d"`
	Sparkline    []float64  `json:"sparkline"`
	// Heartbeat is the outcome of the last few probes, oldest first, true for a
	// probe that succeeded. It is a separate series rather than something derived
	// from Sparkline because Sparkline encodes a failure as a latency of zero —
	// the same zero a genuinely instant success produces. Reading up and down out
	// of those numbers would eventually paint a working sensor red.
	Heartbeat    []bool    `json:"heartbeat"`
	OpenIncident *Incident `json:"open_incident,omitempty"`
	LastIncident *Incident `json:"last_incident,omitempty"`

	// InMaintenance says the sensor is inside a maintenance window right now. The
	// status stays what the probe found — the box really is away — so this is the
	// only thing that tells a card to say "expected" rather than "broken". Without
	// it a scheduled outage and an unplanned one look identical on the dashboard,
	// which is the state of affairs the whole feature exists to fix.
	InMaintenance bool `json:"in_maintenance,omitempty"`
}

// Summary is the aggregate header shown above the dashboard grid.
type Summary struct {
	Total      int       `json:"total"`
	Up         int       `json:"up"`
	Down       int       `json:"down"`
	Warn       int       `json:"warn"`
	Paused     int       `json:"paused"`
	Unknown    int       `json:"unknown"`
	Uptime24h  float64   `json:"uptime_24h"`
	AvgLatency float64   `json:"avg_latency_ms"`
	Probes24h  int       `json:"probes_24h"`
	Since      time.Time `json:"since"`
	// The same three figures over a week.
	//
	// They are here so that a number has something to stand next to: 99.8% is
	// neither good nor bad on its own, and the question a header is asked is
	// almost always "is that normal?". The answer has to come from the same
	// place the twenty-four-hour figure comes from, or the two would not be
	// comparable — so this is the same probe-weighted tally over a longer
	// window, not an average of the per-sensor percentages.
	Uptime7d     float64 `json:"uptime_7d"`
	AvgLatency7d float64 `json:"avg_latency_7d_ms"`
	Probes7d     int     `json:"probes_7d"`
	// Series is the last day of the whole installation, bucketed by the hour,
	// oldest first, for the small curves in the header.
	Series []SummaryPoint `json:"series"`
}

// SummaryPoint is one hour of every sensor taken together — what the whole
// installation did in one bucket of the header's curve.
type SummaryPoint struct {
	TS      time.Time `json:"ts"`
	Uptime  float64   `json:"uptime"`
	Latency float64   `json:"latency_ms"`
	// Probes counts the checks that were counted — maintenance excluded. Maint is
	// how many were left out, so that a bucket drawn as entirely green can still
	// say whether anything happened in it at all.
	Probes int `json:"probes"`
	Maint  int `json:"maint,omitempty"`
}

// Role is what a user is allowed to do. The three are ordered, and every
// permission check in the API is a comparison against that order rather than a
// list of names — a list has to be revisited every time a route is added.
type Role string

const (
	// RoleViewer may read everything the dashboard shows and change nothing.
	RoleViewer Role = "viewer"
	// RoleOperator may additionally act on an outage that has already happened:
	// acknowledge it, un-acknowledge it, pause a sensor, run a probe by hand. It
	// is the role for someone who is on call but does not own the configuration.
	RoleOperator Role = "operator"
	// RoleAdmin may change the configuration, manage users, and read the secrets
	// the dashboard otherwise masks.
	RoleAdmin Role = "admin"
)

// Roles lists every role, most powerful first. The user editor builds its picker
// from this.
var Roles = []Role{RoleAdmin, RoleOperator, RoleViewer}

// Rank orders the roles: viewer 1, operator 2, admin 3. Unknown roles rank 0 and
// therefore satisfy no requirement, which is the safe direction to fail in.
func (r Role) Rank() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleOperator:
		return 2
	case RoleAdmin:
		return 3
	}
	return 0
}

// Valid reports whether r is one of the three known roles.
func (r Role) Valid() bool { return r.Rank() > 0 }

// User is a person who can sign in to the dashboard.
//
// The password hash, the second-factor secret and the session counter are all
// unexported to JSON on purpose: a User value is embedded in API responses, and
// the only way to be sure a secret never leaves the server is for the encoder
// never to see it.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     Role   `json:"role"`
	Disabled bool   `json:"disabled"`

	PasswordHash string `json:"-"`
	// TOTPSecret is the shared secret, base32. It is set as soon as enrolment
	// starts and TOTPEnabled stays false until a code has been confirmed, so
	// "secret set but not enabled" means an enrolment in progress — which is
	// what the QR code endpoint serves.
	TOTPSecret  string `json:"-"`
	TOTPEnabled bool   `json:"totp_enabled"`
	// TOTPLastStep is the most recent time step whose code was accepted. A code
	// belonging to that step or an earlier one is refused, so a code is good for
	// exactly one sign-in rather than for the ninety seconds the drift window
	// spans.
	TOTPLastStep int64 `json:"-"`
	// RecoveryLeft is how many one-time codes remain. It is a counter kept for
	// display; the codes themselves are checked against the recovery_codes rows.
	RecoveryLeft int `json:"recovery_codes_left"`
	// TokenEpoch is bumped whenever every session of this user must stop working:
	// a password change, an administrator's reset, a disable and a re-enable.
	// The session cookie carries the epoch it was issued under, so bumping this
	// is all it takes to sign someone out everywhere.
	TokenEpoch int64 `json:"-"`
	// Prefs is this user's own copy of the interface preferences — theme, accent,
	// language, mode, and how far through the tour they have got.
	Prefs map[string]string `json:"prefs,omitempty"`

	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// Display renders the user as an actor in a log line or an acknowledgement note.
func (u User) Display() string {
	if u.Username == "" {
		return "unknown"
	}
	return u.Username
}

// Interface preference keys, held per user in User.Prefs.
const (
	PrefTheme       = "theme"    // auto | dark | light
	PrefAccent      = "accent"   // accent colour name
	PrefDensity     = "density"  // compact | comfortable
	PrefLayout      = "layout"   // grid | list | tree | wall | map
	PrefLanguage    = "language" // en | de — the catalogues the dashboard ships
	PrefMode        = "mode"     // simple | advanced
	PrefLive        = "live"     // on | off
	PrefTourVersion = "tour_version"
	// PrefWidgets is the dashboard header: which widgets are shown, and in what
	// order. Both in one value, because they are one decision — a comma-separated
	// list of the ids in Widgets. It is a string like every other preference
	// rather than a list, so that it travels the same road as the rest; the
	// whitelist in the API and the registry in the browser are what keep it
	// honest. See Widgets.
	PrefWidgets = "widgets"
)

// Layouts are the ways the dashboard can show the same set of sensors, in the
// order the switcher offers them.
//
// The list is here, and not only in the API's whitelist, because it lives twice:
// once in Go and once in the dashboard's own list of buttons. That is the same
// duplication as model.SensorTypes, with the same failure — a layout the server
// will store and the dashboard never offers is a setting that silently does
// nothing, and one the dashboard offers and the server refuses is a button that
// appears to work until the page is reloaded. Naming it here lets a browser test
// compare the two.
var Layouts = []string{"grid", "list", "tree", "wall", "map"}

// Widgets are the pieces the dashboard header can be built from, in the order the
// picker lists them.
//
// Same duplication as Layouts, and the same failure if the two drift: a widget the
// server will store and the browser cannot draw is a blank tile, and one the
// browser offers and the server refuses is a setting that appears to save and does
// not. The registry in internal/web/assets/widgets.js is the other half of this
// list; both are compared by a test.
//
// The order here is the order of the picker, grouped by what the widget is for:
// the lamp and the figures first, then the clock, then the lists, then the map and
// the shortcuts. It is not the order on screen — that is whatever the person
// dragged it into, and it is stored per account.
var Widgets = []string{
	"status", "uptime", "latency", "probes", "since",
	"clock",
	"recent", "slowest", "flakiest",
	"mini-map", "shortcuts",
}

// DefaultWidgets is the header an account has before it chooses one: the lamp with
// its counts, and the four figures beside it.
//
// It is exactly what the dashboard showed before the header was configurable, and
// that is the point — the change adds a choice without moving anything for someone
// who never makes one.
const DefaultWidgets = "status,uptime,latency,probes,since"

// ParseWidgets reads a stored widget list. An entry the server does not know is
// dropped rather than kept: the value is a rendering instruction, and a name no
// browser can draw would become an empty tile that cannot be removed from the
// picker either. Duplicates are dropped too, since the picker has one row per
// widget and a second copy could not be chosen.
func ParseWidgets(value string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(Widgets))
	for _, id := range strings.Split(value, ",") {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		for _, known := range Widgets {
			if known == id {
				seen[id] = true
				out = append(out, id)
				break
			}
		}
	}
	return out
}

// DefaultPrefs is what an interface preference falls back to when a user has
// never chosen one. It is a function rather than a package-level map so that a
// caller cannot accidentally write into the defaults for everybody.
func DefaultPrefs() map[string]string {
	return map[string]string{
		PrefTheme:   "auto",
		PrefAccent:  "azure",
		PrefDensity: "comfortable",
		PrefLayout:  "grid",
		// English, and deliberately not whatever Accept-Language says. The
		// dashboard never guesses: the documentation, the installer and the release
		// notes are in English, so that is where a first-time user starts, and
		// Settings has the switch for anybody who would rather read something else.
		PrefLanguage: "en",
		// Live is on by default. The point of a dashboard is to be right now, and
		// a number that is a minute old is the thing this setting exists to fix.
		// It can be turned off — on a wall display nobody is watching, or on a
		// metered connection — and the slow refresh underneath it keeps running
		// either way, so "off" means quiet, not stale.
		PrefLive: "on",
		// Neither interface mode is a default in the sense of being recommended —
		// the setup screen asks. This is only what a session that has no user
		// behind it (an API key, or nobody at all) is told.
		PrefMode:        "advanced",
		PrefTourVersion: "0",
		// The header, as above: the lamp and the four figures, which is what the
		// dashboard showed before anyone could rearrange it.
		PrefWidgets: DefaultWidgets,
	}
}

// PrefKeys lists the preference keys the API will accept.
var PrefKeys = []string{
	PrefTheme, PrefAccent, PrefDensity, PrefLayout, PrefLanguage, PrefMode, PrefLive, PrefTourVersion,
	PrefWidgets,
}

// Pref returns one of the user's preferences, falling back to the default when
// they have never chosen one.
func (u User) Pref(key string) string {
	if v, ok := u.Prefs[key]; ok && v != "" {
		return v
	}
	return DefaultPrefs()[key]
}

// Setting keys persisted in the settings table. The settings table now holds
// installation-wide facts only; anything belonging to a person lives on their
// user row.
const (
	// SettingLegacyPasswordHash is the single password of a pre-multi-user
	// installation. Nothing reads it any more — it is migrated into the first
	// user on upgrade and deleted — but the doctor still looks for it, so that
	// an installation whose migration could not run says so.
	SettingLegacyPasswordHash = "auth.password_hash"
	SettingAPIKey             = "auth.api_key"
	SettingSessionSecret      = "auth.session_secret"
	SettingRetentionRaw       = "retention.raw"
	// SettingTileURL and SettingTileAttribution configure the map. They are
	// installation-wide rather than per user, so they live here and not in the
	// preferences: the tiles a homelab is allowed to fetch is an operator's
	// decision about the installation's network, not a matter of taste.
	SettingTileURL         = "map.tile_url"
	SettingTileAttribution = "map.attribution"
	// SettingMapBorders is whether the map draws the borders and the names that
	// shipped in the binary. It is installation-wide like the two above it, and for
	// the same reason: what the map draws is the same map for everybody who looks at
	// it, and a per-user version would mean two readers describing the same screen
	// differently.
	SettingMapBorders = "map.borders"
)

// The map's defaults, which are both empty, and empty means "fetch nothing".
//
// They used to name OpenStreetMap, on the reasoning that a map view with nobody to
// run a tile server should still show a map. That reasoning expired: OSM's tile
// usage policy stopped permitting third-party applications in 2025, and their
// servers now answer a request from one with a block notice — under HTTP 200, so
// the browser counts it as a tile that loaded and the reader is shown an apology
// where a coastline should be. The dashboard draws the world itself instead, from
// a dataset in the binary, and the field below stays for anybody who runs a tile
// server of their own or has found one that wants the traffic.
//
// The constants stay, and stay referenced from the docs, because RetiredTileURL is
// what an installation that saved the old default has in its row.
const (
	DefaultTileURL         = ""
	DefaultTileAttribution = ""
	// DefaultMapBorders is on, and the value is "on" rather than a bool for the same
	// reason PrefLive's is: this project stores a yes/no setting as those two words.
	// A missing row means the default, which means an installation that has never
	// opened the panel gets the borders — the map that was asked for.
	DefaultMapBorders = "on"
)

// RetiredTileURL is the default tile address this project used to ship.
//
// The form wrote the value it was showing into the row on save, so an installation
// that opened Settings → Map once and pressed Save has it stored as though somebody
// chose it. Every one of those installations would keep asking OSM for tiles, and
// keep being shown the block notice, after the default moved out from under them.
// The migration deletes the row; this recognises it, in case the same value arrives
// again — restored from a backup taken before the migration ran, or typed by hand.
const RetiredTileURL = "https://tile.openstreetmap.org/{z}/{x}/{y}.png"

// IsRetiredTileURL reports whether a stored tile address is the one this project
// used to default to. The comparison ignores surrounding whitespace because the
// value travelled through an HTML form.
func IsRetiredTileURL(s string) bool {
	return strings.TrimSpace(s) == RetiredTileURL
}

// TileURLTemplate is what the map's placeholder substitution understands. A URL
// containing anything else is a typo, and a typo here fails as a blank map rather
// than as an error, so it is worth rejecting on the way in.
var TileURLPlaceholders = []string{"{z}", "{x}", "{y}", "{s}"}

// SplitTags turns a stored comma-separated tag list into a slice.
func SplitTags(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// JoinTags normalises and joins tags for storage.
func JoinTags(tags []string) string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	return strings.Join(out, ",")
}
