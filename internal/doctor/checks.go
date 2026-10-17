package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/j000k3r/probeglass/internal/config"
	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/notify"
	"github.com/j000k3r/probeglass/internal/privdrop"
	"github.com/j000k3r/probeglass/internal/sensor"
	"github.com/j000k3r/probeglass/internal/store"
)

// run carries the state of one pass.
type run struct {
	opts   Options
	cfg    *config.Config
	client *http.Client

	findings []Finding

	// st is the read-only handle. It is nil when the database could not be
	// opened, and every check that needs one is skipped in that case.
	st *store.Store

	// sensors and channels are what is stored, loaded once by the sensor that
	// needs them.
	sensors  []model.Sensor
	channels []model.Channel
}

func (r *run) add(level Level, label, detail string) {
	r.findings = append(r.findings, Finding{Level: level, Label: label, Detail: detail})
}

func (r *run) addHint(level Level, label, detail, hint string) {
	r.findings = append(r.findings, Finding{Level: level, Label: label, Detail: detail, Hint: hint})
}

// ---------------------------------------------------------------------------
// the things that do not need a database
// ---------------------------------------------------------------------------

// checkBuild names the binary. It never fails; knowing which version produced
// the rest of the report is the first thing anyone reading it needs.
func (r *run) checkBuild() {
	r.add(OK, "build", "probeglass "+model.BuildInfo())
}

func (r *run) checkConfig() {
	if r.opts.ConfigErr != nil {
		r.addHint(Fail, "config", r.opts.ConfigErr.Error(),
			"the daemon will not start until this file can be read")
		return
	}
	if err := r.cfg.Validate(); err != nil {
		r.addHint(Fail, "config", err.Error(),
			"the daemon refuses to start on an invalid configuration")
		return
	}
	if r.cfg.Path == "" {
		r.add(OK, "config", "no configuration file; using built-in defaults")
		return
	}
	r.add(OK, "config", fmt.Sprintf("%s (%s, %s)",
		r.cfg.Path,
		plural(len(r.cfg.Sensors), "sensor"),
		plural(len(r.cfg.Channels), "channel")))
}

// checkDataDir proves the daemon can persist results.
//
// The daemon creates its data directory itself, so one that does not exist yet
// is not a problem — the question is whether the nearest ancestor that does
// exist is writable. Testing the directory as it is would report a failure for
// an installation that is about to work.
//
// The identity that matters is the daemon's, not this one. doctor is a separate
// process: under docker exec it runs as whatever the image's user is — root — while
// the daemon takes the directory over as root and then drops to PUID:PGID before it
// opens anything. Both are printed, because "not writable by uid 0" and "not
// writable by uid 500" lead to two different fixes.
func (r *run) checkDataDir() {
	dir := filepath.Dir(r.cfg.DBPath)
	uid := os.Getuid()

	// Read for the target identity only. ParseSettings is pure, and doctor must not
	// do anything else from this package — a diagnostic tool that quietly repairs the
	// folder it was asked to report on is worse than no diagnostic tool.
	settings, settingsErr := privdrop.ParseSettings(os.Getenv)
	if settingsErr != nil {
		// A malformed value is ignored by a process that is not root and fatal to one
		// that is, which is the rule the daemon itself applies. Whether doctor is root
		// decides which of those two this is.
		level, hint := Warn, "this process is not root, so PUID, PGID and UMASK are ignored anyway;"+
			" the container's daemon does start as root, and there the same value stops the start"
		if uid == 0 {
			level, hint = Fail, "the daemon refuses to start on a value it cannot understand"
		}
		r.addHint(level, "container user", settingsErr.Error(), hint)
	}

	probe, missing := dir, false
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		missing = true
		parent := filepath.Dir(probe)
		if parent == probe {
			r.addHint(Fail, "data dir",
				dir+" does not exist, and neither does any ancestor to create it in",
				"point -data or PROBEGLASS_DATA_DIR at a directory that exists")
			return
		}
		probe = parent
	}

	f, err := os.CreateTemp(probe, ".doctor-*")
	if err != nil {
		if uid == 0 {
			// Root writes nearly anywhere it likes, so this is not about ownership
			// at all: the filesystem is not taking writes.
			r.addHint(Fail, "data dir",
				fmt.Sprintf("%s is not writable even as root: %v", probe, err),
				"the mount is read-only or full rather than owned by the wrong user;"+
					" PUID and PGID will not help here")
			return
		}
		r.addHint(Fail, "data dir",
			fmt.Sprintf("%s is not writable by uid %d: %v", probe, uid, err),
			fmt.Sprintf("started as root, the daemon would have taken this directory over and dropped to PUID:PGID"+
				" before opening anything, so this means it did not start as root — a --user override, or a runtime"+
				" that will not run it as root. Either let it start as root (the image's default) so it can fix the"+
				" folder itself, or hand the folder to the user that runs it: chown -R %d:%d <folder>."+
				" PUID and PGID cannot help while the process is not root: an unprivileged process cannot change user",
				uid, os.Getgid()))
		return
	}
	name := f.Name()
	f.Close()
	os.Remove(name)

	// Who the verdict is about. Only a root doctor can be looking at the daemon's
	// folder while being a different user from it.
	whom := fmt.Sprintf("by uid %d", uid)
	if uid == 0 && settings.UID != 0 {
		whom = fmt.Sprintf("as root; the daemon runs as uid %d", settings.UID)
	}

	if missing {
		r.add(OK, "data dir", fmt.Sprintf("%s (does not exist yet, created on first start; %s is writable %s)",
			dir, probe, whom))
		return
	}
	r.add(OK, "data dir", fmt.Sprintf("%s (writable %s)", dir, whom))
}

// openDatabase stats the file and opens it read-only. It reports whether the
// checks that need a connection should run at all.
func (r *run) openDatabase(ctx context.Context) bool {
	info, err := os.Stat(r.cfg.DBPath)
	if err != nil {
		if os.IsNotExist(err) {
			// A fresh install is a legitimate state, and the first start of the
			// container races this command. Calling it a failure would make a
			// healthy new deployment look broken.
			r.addHint(Warn, "database", r.cfg.DBPath+" does not exist yet",
				"the daemon creates it on first start; `probeglass seed --demo` fills a demo one")
			return false
		}
		r.addHint(Fail, "database", fmt.Sprintf("%s: %v", r.cfg.DBPath, err),
			"check the path and its permissions")
		return false
	}
	if info.IsDir() {
		r.addHint(Fail, "database", r.cfg.DBPath+" is a directory, not a file",
			"db_path has to name the database file")
		return false
	}

	st, err := store.OpenReadOnly(r.cfg.DBPath, quiet())
	if err != nil {
		r.addHint(Fail, "database", fmt.Sprintf("could not open %s: %v", r.cfg.DBPath, err),
			"if the daemon is stopped, the directory has to be writable so SQLite can open the WAL index")
		return false
	}
	r.st = st

	detail := fmt.Sprintf("%s, %s", r.cfg.DBPath, humanBytes(info.Size()))
	if n, err := st.ProbeCount(ctx); err == nil {
		detail += fmt.Sprintf(", %s", plural(int(n), "probe"))
	}
	r.add(OK, "database", detail)
	return true
}

// checkIntegrity runs SQLite's own consistency check. It is the only thing here
// that reads the whole file, so it is also the only one that can be slow.
func (r *run) checkIntegrity(ctx context.Context) {
	rows, err := r.st.DB().QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		r.add(Fail, "integrity", "quick_check could not run: "+err.Error())
		return
	}
	defer rows.Close()

	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			r.add(Fail, "integrity", "quick_check could not be read: "+err.Error())
			return
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		// A timeout is a big database, not a broken one. Reporting it as a
		// failure would send somebody hunting for a corruption that is not there.
		if ctx.Err() != nil {
			r.addHint(Warn, "integrity", "quick_check did not finish in the time allowed",
				"raise -timeout; verifying a large database takes a while")
			return
		}
		r.add(Fail, "integrity", "quick_check failed: "+err.Error())
		return
	}
	if len(problems) > 0 {
		r.addHint(Fail, "integrity", strings.Join(problems, "; "),
			"the file is damaged; restore a backup, or start from a fresh database")
		return
	}
	r.add(OK, "integrity", "quick_check ok")
}

// checkSchema compares the file against this binary. Both directions matter, and
// they are not symmetric: an old database is migrated on the next start, a new
// one is a binary that needs upgrading.
func (r *run) checkSchema(ctx context.Context) {
	got, err := r.st.SchemaVersion(ctx)
	if err != nil {
		// A file with no schema_migrations table is not a ProbeGlass database
		// at all, or is truncated. That falls out of the query for free, so
		// there is no second way of asking the same question.
		r.addHint(Fail, "schema", fmt.Sprintf("could not read the schema version: %v", err),
			"this does not look like a ProbeGlass database; check db_path")
		return
	}
	want := store.LatestSchemaVersion()
	switch {
	case got == want:
		r.add(OK, "schema", fmt.Sprintf("version %d", got))
	case got < want:
		r.addHint(Warn, "schema", fmt.Sprintf("version %d, this binary migrates to %d", got, want),
			"the next start of the daemon applies the missing migration")
	default:
		r.addHint(Fail, "schema", fmt.Sprintf("version %d, but this binary only knows %d", got, want),
			"this database was written by a newer ProbeGlass; upgrade the binary")
	}
}

// checkUsers reports who can sign in. It runs on a database opened read-only, so
// it has to cope with a file that predates the users table: the doctor describes
// a broken installation, it does not migrate it.
func (r *run) checkUsers(ctx context.Context) {
	has, err := r.st.HasTable(ctx, "users")
	if err != nil {
		r.add(Fail, "users", "the schema could not be inspected: "+err.Error())
		return
	}
	if !has {
		r.addHint(Warn, "users", "no user accounts yet: this database predates them",
			"the next start of the daemon creates an administrator from the password "+
				"already stored, or opens the setup screen if there is none")
		return
	}

	users, err := r.st.ListUsers(ctx)
	if err != nil {
		r.add(Fail, "users", "the accounts could not be read: "+err.Error())
		return
	}

	// A leftover global password means the migration created accounts but could
	// not finish, or that somebody put the setting back. Either way it is a second
	// answer to "who may sign in", and this is the place to notice it.
	if legacy := r.st.Setting(ctx, model.SettingLegacyPasswordHash, ""); legacy != "" {
		r.addHint(Warn, "users", "an old global dashboard password is still stored",
			"nothing reads it any more; remove the "+model.SettingLegacyPasswordHash+
				" setting so there is one source of truth for who can sign in")
	}

	var enabled, admins, adminsWithoutFactor int
	for _, u := range users {
		if u.Disabled {
			continue
		}
		enabled++
		if u.Role == model.RoleAdmin {
			admins++
			if !u.TOTPEnabled {
				adminsWithoutFactor++
			}
		}
	}

	switch {
	case len(users) == 0:
		r.addHint(Warn, "users", "the accounts table is empty",
			"open the dashboard to run the setup screen again")
		return
	case enabled == 0:
		r.addHint(Fail, "users", fmt.Sprintf("%s, all of them disabled",
			plural(len(users), "account")),
			"nobody can sign in; enable one with an administrator's help, "+
				"or delete the database file to start over")
		return
	}

	detail := fmt.Sprintf("%s, %s", plural(enabled, "account"), plural(admins, "active administrator"))
	r.add(OK, "users", detail)
	if adminsWithoutFactor > 0 {
		r.addHint(Warn, "second factor",
			fmt.Sprintf("%s without a second factor", plural(adminsWithoutFactor, "administrator")),
			"the dashboard usually runs over plain HTTP on a LAN, where a password alone "+
				"is worth little; enrol one under Settings, Two-factor authentication")
	}
}

// checkContents validates what is stored. A record that no longer satisfies its
// own validator is a record this version cannot have written, and one the
// scheduler will refuse to run.
func (r *run) checkContents(ctx context.Context) {
	sensors, err := r.st.ListSensors(ctx)
	if err != nil {
		r.add(Fail, "contents", "the stored sensors could not be read: "+err.Error())
		return
	}
	channels, err := r.st.ListChannels(ctx)
	if err != nil {
		r.add(Fail, "contents", "the stored channels could not be read: "+err.Error())
		return
	}
	r.sensors, r.channels = sensors, channels

	var bad []string
	for _, c := range sensors {
		if err := sensor.Validate(c); err != nil {
			bad = append(bad, fmt.Sprintf("sensor %q: %v", c.Name, err))
		}
	}
	for _, ch := range channels {
		if err := notify.ValidateChannel(ch); err != nil {
			bad = append(bad, fmt.Sprintf("channel %q: %v", ch.Name, err))
		}
	}
	if len(bad) > 0 {
		r.addHint(Fail, "contents", strings.Join(bad, "; "),
			"a stored record no longer validates; recreate it in the dashboard")
		return
	}

	// A sensor pointing at a channel that no longer exists alerts nobody, and
	// nothing else in the product says so out loud.
	known := make(map[int64]bool, len(channels))
	for _, ch := range channels {
		known[ch.ID] = true
	}
	var dangling []string
	for _, c := range sensors {
		for _, id := range c.Channels {
			if !known[id] {
				dangling = append(dangling, strconv.Quote(c.Name))
				break
			}
		}
	}

	enabled := 0
	for _, c := range sensors {
		if c.Enabled {
			enabled++
		}
	}
	detail := fmt.Sprintf("%s, %s, %d enabled",
		plural(len(sensors), "sensor"), plural(len(channels), "channel"), enabled)

	if len(dangling) > 0 {
		r.addHint(Warn, "contents",
			detail+"; pointing at a deleted alert channel: "+strings.Join(dangling, ", "),
			"the channel was removed; edit those sensors to attach another one")
		return
	}
	if len(sensors) > 0 && enabled == 0 {
		r.addHint(Warn, "contents", detail,
			"every sensor is paused, so nothing is being monitored")
		return
	}
	r.add(OK, "contents", detail)
}

// checkConfigSpecs validates the sensors and channels declared in the file, and
// notices when one of them has not reached the database.
//
// It deliberately builds them through config.SensorSpec.ToSensor, the same
// conversion serve uses: a file doctor accepts has to be a file serve applies.
func (r *run) checkConfigSpecs(ctx context.Context) {
	if len(r.cfg.Sensors) == 0 && len(r.cfg.Channels) == 0 && len(r.cfg.Sites) == 0 {
		return
	}

	var bad []string
	for _, spec := range r.cfg.Sensors {
		c := spec.ToSensor()
		store.NormaliseSensor(&c)
		if err := sensor.Validate(c); err != nil {
			bad = append(bad, fmt.Sprintf("sensor %q: %v", spec.Name, err))
		}
		// The icon is checked here as well as in serve, and for the same reason the
		// rest of the file is: an `upload:` reference to an image this installation
		// does not have would stop the daemon at the next start, and doctor exists
		// to say that in advance rather than in a container that will not come up.
		// Without a database there is nothing to look the id up in, and the check is
		// skipped rather than reported as a failure that is not one.
		if r.st != nil {
			if err := r.st.ValidateIconRef(ctx, c.Icon); err != nil {
				bad = append(bad, fmt.Sprintf("sensor %q: %v", spec.Name, err))
			}
		}
	}
	for _, spec := range r.cfg.Sites {
		site := spec.ToSite()
		store.NormaliseSite(&site)
		if err := store.ValidateSite(site); err != nil {
			bad = append(bad, fmt.Sprintf("site %q: %v", spec.Name, err))
		}
		if r.st != nil {
			if err := r.st.ValidateIconRef(ctx, site.Icon); err != nil {
				bad = append(bad, fmt.Sprintf("site %q: %v", spec.Name, err))
			}
		}
	}
	for _, spec := range r.cfg.Channels {
		ch := spec.ToChannel()
		store.NormaliseChannel(&ch)
		if err := notify.ValidateChannel(ch); err != nil {
			bad = append(bad, fmt.Sprintf("channel %q: %v", spec.Name, err))
		}
	}
	if len(bad) > 0 {
		r.addHint(Fail, "config sensors", strings.Join(bad, "; "),
			"the daemon will refuse to start until these are fixed")
		return
	}

	if missing := r.declaredButAbsent(ctx); len(missing) > 0 {
		r.addHint(Warn, "config sensors",
			strings.Join(missing, ", ")+" declared in the configuration file but not in the database",
			"restart the daemon to apply the configuration")
		return
	}

	// A sensor may name a site the file never declares — the daemon creates the
	// row on the way past, so an unknown one is not an error. It is still worth
	// saying, because until the daemon runs again the sensor cannot be stored at
	// all: the foreign key will refuse a site that does not exist yet, and the
	// only symptom is a sensor that quietly is not there.
	if unknown := r.sitesNamedButAbsent(ctx); len(unknown) > 0 {
		r.addHint(Warn, "config sites",
			strings.Join(unknown, ", ")+" named by a sensor but not in the database",
			"restart the daemon to apply the configuration")
		return
	}

	r.add(OK, "config sensors", fmt.Sprintf("%s and %s from the file validated",
		plural(len(r.cfg.Sensors), "sensor"), plural(len(r.cfg.Channels), "channel")))
}

// sitesNamedButAbsent lists sites that config sensors point at and that the
// database does not have. It is separate from declaredButAbsent because a site
// named by a sensor was never declared anywhere — the daemon synthesises it — so
// the two are missing for different reasons and are worth saying apart.
func (r *run) sitesNamedButAbsent(ctx context.Context) []string {
	if r.st == nil {
		return nil
	}
	sites, err := r.st.ListSites(ctx)
	if err != nil {
		return nil
	}
	stored := make(map[string]bool, len(sites))
	for _, s := range sites {
		stored[s.Name] = true
	}

	seen := map[string]bool{}
	var missing []string
	for _, spec := range r.cfg.Sensors {
		name := strings.TrimSpace(spec.Site)
		if name == "" || stored[name] || seen[name] {
			continue
		}
		seen[name] = true
		missing = append(missing, "site "+strconv.Quote(name))
	}
	return missing
}

// declaredButAbsent lists declarations that have not been applied yet. It reads
// the tables itself rather than reusing checkContents' lists, so a failure there
// cannot turn into a false "everything is missing" here.
func (r *run) declaredButAbsent(ctx context.Context) []string {
	if r.st == nil {
		return nil
	}
	sensors, err := r.st.ListSensors(ctx)
	if err != nil {
		return nil
	}
	channels, err := r.st.ListChannels(ctx)
	if err != nil {
		return nil
	}
	sites, err := r.st.ListSites(ctx)
	if err != nil {
		return nil
	}

	storedSensors := make(map[string]bool, len(sensors))
	for _, c := range sensors {
		storedSensors[c.Name] = true
	}
	storedChannels := make(map[string]bool, len(channels))
	for _, ch := range channels {
		storedChannels[ch.Name] = true
	}
	storedSites := make(map[string]bool, len(sites))
	for _, s := range sites {
		storedSites[s.Name] = true
	}

	var missing []string
	for _, spec := range r.cfg.Sensors {
		if !storedSensors[spec.Name] {
			missing = append(missing, "sensor "+strconv.Quote(spec.Name))
		}
	}
	for _, spec := range r.cfg.Sites {
		if !storedSites[spec.Name] {
			missing = append(missing, "site "+strconv.Quote(spec.Name))
		}
	}
	for _, spec := range r.cfg.Channels {
		if !storedChannels[spec.Name] {
			missing = append(missing, "channel "+strconv.Quote(spec.Name))
		}
	}
	return missing
}

// checkDaemon finds out whether the process is up and answering.
//
// The polarity here is the opposite of the one in serve. There, "the port is
// taken" is an error; here it is the healthy state, because doctor is normally
// run with docker exec inside a container whose daemon is already running. A
// refusal is what is worth a warning.
func (r *run) checkDaemon(ctx context.Context) {
	url := "http://" + r.opts.HealthAddr + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		r.add(Fail, "daemon", "could not build the health request: "+err.Error())
		return
	}

	resp, err := r.client.Do(req)
	if err != nil {
		r.addHint(Warn, "daemon", "nothing is listening on "+r.opts.HealthAddr,
			"start the daemon, or pass -listen if it is on a different address")
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		r.add(Warn, "daemon", fmt.Sprintf("%s answered, but the body could not be read: %v",
			r.opts.HealthAddr, err))
		return
	}

	// A non-200 from this endpoint is the dashboard's own error body, which for
	// example a failing database produces. Report it as the daemon saying so,
	// rather than as a stranger on the port.
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &failure) == nil && failure.Error != "" {
			r.addHint(Fail, "daemon",
				fmt.Sprintf("the daemon answered HTTP %d: %s", resp.StatusCode, failure.Error),
				"the process is up but cannot serve; the container log has the detail")
			return
		}
		r.addHint(Warn, "daemon",
			fmt.Sprintf("something is listening on %s, but it is not this dashboard", r.opts.HealthAddr),
			fmt.Sprintf("it answered HTTP %d", resp.StatusCode))
		return
	}

	var health struct {
		Status  string `json:"status"`
		Version string `json:"version"`
		Uptime  string `json:"uptime"`
	}
	if err := json.Unmarshal(body, &health); err != nil || health.Status != "ok" {
		r.addHint(Warn, "daemon",
			fmt.Sprintf("something is listening on %s, but it is not this dashboard", r.opts.HealthAddr),
			"it answered 200 without the health payload")
		return
	}
	r.add(OK, "daemon", fmt.Sprintf("running on %s, version %s, up %s",
		r.opts.HealthAddr, health.Version, health.Uptime))
}

// checkICMP reports the capability, not the reachability: it opens a socket and
// closes it again without sending anything. It stays silent unless a ping sensor
// exists, because on a host that runs none the answer is pure noise.
func (r *run) checkICMP() {
	if !r.hasEnabledPingSensor() {
		return
	}
	if err := sensor.ICMPAvailable(); err != nil {
		// Never a failure. Every other sensor type works without it, and the ping
		// sensor already reports this per probe, in its own words.
		r.addHint(Warn, "icmp", err.Error(),
			"only ping sensors are affected; they will report the same thing per probe")
		return
	}
	r.add(OK, "icmp", "an ICMP socket can be opened")
}

func (r *run) hasEnabledPingSensor() bool {
	for _, c := range r.sensors {
		if c.Type == model.SensorPing && c.Enabled {
			return true
		}
	}
	for _, spec := range r.cfg.Sensors {
		if model.SensorType(spec.Type) != model.SensorPing {
			continue
		}
		if spec.Enabled == nil || *spec.Enabled {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

// plural renders a count with its noun. Every noun this report uses is regular,
// which is why the plural is built by adding an s.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// humanBytes formats a size the way the dashboard does, in binary units.
func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(n)
	i := -1
	for value >= 1024 && i < len(units)-1 {
		value /= 1024
		i++
	}
	// One decimal, except for the round numbers where it is just noise.
	if value == math.Trunc(value) {
		return fmt.Sprintf("%.0f %s", value, units[i])
	}
	return fmt.Sprintf("%.1f %s", value, units[i])
}
