// Package doctor inspects a Weatherglass installation and reports what is wrong
// with it.
//
// It exists because the hardest failures to debug are the ones where nothing
// happens: the dashboard does not load, a sensor never turns green, no alert ever
// arrives. In a container there is no shell and no curl, so the user gets one
// command — `docker exec <container> weatherglass doctor` — and it has to answer
// the question on its own.
//
// Two rules shape everything here:
//
//   - It changes nothing. The database is opened read-only; nothing is created,
//     nothing is migrated, nothing is repaired. A diagnostic that quietly fixes
//     what it was asked to report is worse than no diagnostic at all: it reports
//     success for a problem it has just removed.
//   - It never sends an alert. Channel configuration is validated; delivery is
//     not attempted. Firing a real message into a chat room from a machine
//     somebody is trying to debug is not a thing to do.
//
// Run never returns an error. A fatal condition becomes a failure finding,
// because the point is to keep reporting: the situation doctor exists for is one
// where the daemon will not start, and stopping at the first problem would hide
// the rest of them.
package doctor

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/j000k3r/weatherglass/internal/config"
)

// Level says how bad a finding is.
type Level int

const (
	// OK means the check passed.
	OK Level = iota
	// Warn is worth knowing and does not stop the installation working.
	Warn
	// Fail means the daemon cannot do its job.
	Fail
)

// String renders the level the way the report shows it: lowercase, as a status
// word, so it cannot be mistaken for a log level in a pasted transcript.
func (l Level) String() string {
	switch l {
	case OK:
		return "ok"
	case Warn:
		return "warn"
	case Fail:
		return "fail"
	default:
		return "?"
	}
}

// Finding is one line of the report.
type Finding struct {
	Level  Level
	Label  string // the short left-hand name, for example "database"
	Detail string // what was found
	Hint   string // what to do about it, printed on its own line
}

// Report is everything one run found.
type Report struct {
	Findings []Finding
}

// Failed reports whether anything was bad enough to stop the installation
// working. Warnings alone are not a failure: a fresh install warns, and it is
// perfectly healthy.
func (r Report) Failed() bool {
	for _, f := range r.Findings {
		if f.Level == Fail {
			return true
		}
	}
	return false
}

// Counts returns how many findings there are at each level.
func (r Report) Counts() (ok, warn, fail int) {
	for _, f := range r.Findings {
		switch f.Level {
		case OK:
			ok++
		case Warn:
			warn++
		case Fail:
			fail++
		}
	}
	return ok, warn, fail
}

// Options is what the caller knows and doctor cannot work out for itself.
type Options struct {
	// Config is the resolved configuration. It is never nil: config.Resolve
	// always returns a usable one, even when the file could not be read, which
	// is exactly the case a diagnostic has to survive.
	Config *config.Config

	// ConfigErr is what resolving the configuration file reported, if anything.
	// It becomes a finding rather than an early return, because a broken file is
	// one of the main reasons to run this at all.
	ConfigErr error

	// HealthAddr is the host:port the daemon should be answering on, already
	// localised: a wildcard bind address has been replaced with loopback,
	// because a wildcard address cannot be dialled.
	HealthAddr string

	// Timeout bounds the whole run, and the network checks in particular.
	Timeout time.Duration
}

// Run performs every check and returns the report.
func Run(ctx context.Context, opts Options) Report {
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	r := &run{
		opts:   opts,
		cfg:    opts.Config,
		client: &http.Client{Timeout: 5 * time.Second},
	}

	// Cheap and local first, the database second, the network last: the order is
	// also the order in which the reader can act on what they are told.
	r.checkBuild()
	r.checkConfig()
	r.checkDataDir()
	if r.openDatabase(ctx) {
		defer r.st.Close()
		r.checkIntegrity(ctx)
		r.checkSchema(ctx)
		r.checkUsers(ctx)
		r.checkContents(ctx)
	}
	r.checkConfigSpecs(ctx)
	r.checkDaemon(ctx)
	r.checkICMP()

	return Report{Findings: r.findings}
}

// quiet returns a logger that discards everything. The report is doctor's
// output; a log line beside it would only be noise. A logger is still wanted
// because store.OpenReadOnly takes one it will never use.
func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
