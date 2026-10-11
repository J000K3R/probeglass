// Package privdrop gives the data directory to the user the daemon will run as,
// and then becomes that user, before the database or the listener exists.
//
// It exists because a bind mount brings the *host* folder's ownership along with
// it. The platforms this ships to create that folder as root, so a daemon that is
// not root cannot write inside it — which SQLite reports as "unable to open
// database file (14)" and says nothing else about. Starting as root for the few
// milliseconds it takes to fix that, and then leaving root for good, is what every
// image of this kind on those platforms does, and it is the only way to fix the
// folder from inside the container: the folder belongs to root, and only root can
// change that.
//
// The order is the whole point. Take ownership first, then give up root, and
// never the other way round: after a privileged setuid there is no way back, and
// nothing may be opened before it happens.
package privdrop

import (
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// DefaultUID and DefaultGID are the identity the platforms this ships to
	// create an appdata folder with, and the one every other container there runs
	// as. 500 rather than the image's own 65532 for exactly that reason: it is the
	// number that makes a fresh install work with nothing set.
	DefaultUID = 500
	DefaultGID = 500

	// DefaultUmask is the mask every other image on those platforms uses for the
	// files it creates.
	DefaultUmask = 0o022
)

// The three variables, named once so that the parsing and the "this is set but
// cannot take effect" warning cannot drift apart.
const (
	envUID   = "PUID"
	envGID   = "PGID"
	envUmask = "UMASK"
)

// Settings is the identity the process will end up as.
type Settings struct {
	UID   int
	GID   int
	Umask int
}

// ParseSettings reads PUID, PGID and UMASK.
//
// getenv is a parameter rather than os.Getenv so that the parsing can be tested
// without a process environment to set, and so that a test can say what a typo
// does without a typo having to be possible.
//
// A value that cannot be understood is an error, not a quiet fallback to the
// default. These three decide which user the process becomes, and a PUID that was
// ignored without a word is the exact failure this package was written to remove:
// the daemon would run as 500 while the operator believed it ran as what they
// typed. That is the treatment WEATHERGLASS_LOG_LEVEL gets; the retention
// durations, which swallow a typo and keep the default, are the counter-example.
func ParseSettings(getenv func(string) string) (Settings, error) {
	s := Settings{UID: DefaultUID, GID: DefaultGID, Umask: DefaultUmask}

	for _, v := range []struct {
		key  string
		into *int
	}{
		{envUID, &s.UID},
		{envGID, &s.GID},
	} {
		raw := strings.TrimSpace(getenv(v.key))
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > math.MaxInt32 {
			return s, fmt.Errorf("invalid %s %q: want a numeric user or group id, or leave it unset for %d",
				v.key, raw, *v.into)
		}
		*v.into = n
	}

	// Octal, because that is how a mask is written wherever it is written down:
	// 022, 0022 and 22 all mean the same thing here. "18" is rejected, and that is
	// the point of parsing it strictly — it is what a compose file produces when
	// 022 is written without quotes around it, and 0018 is not what anybody typed.
	if raw := strings.TrimSpace(getenv(envUmask)); raw != "" {
		n, err := strconv.ParseUint(raw, 8, 32)
		if err != nil || n > 0o777 {
			return s, fmt.Errorf("invalid %s %q: want an octal mask between 000 and 777,"+
				" in quotes so that the leading zero survives (\"022\")", envUmask, raw)
		}
		s.Umask = int(n)
	}

	return s, nil
}

// Options is what the preflight needs to know about this installation.
type Options struct {
	// DataDir is created when it is missing and handed to the target user,
	// together with everything inside it. Required.
	DataDir string

	// DBPath is handed over as well when it lies outside DataDir. A database on a
	// different volume is the one case the directory alone does not cover.
	DBPath string

	// Logger is where the outcome is reported. Every caller in the program has
	// one; a nil one is accepted so that a test can call this without one.
	Logger *slog.Logger
}

// Report says what was decided and done, so that the caller can log a true line
// on the runs where nothing happened — which is every run outside a container.
type Report struct {
	// Applied is false when the process was not root, or when PUID and PGID asked
	// for root to be kept.
	Applied bool

	// Reason is why, in one clause, for that log line.
	Reason string

	// UID and GID are the identity the process has once Apply has returned.
	UID int
	GID int

	// LostCaps names capabilities the process had before the drop and does not
	// have now. CAP_NET_RAW is the one that matters: it is what a ping sensor
	// opens a raw socket with. Empty unless a drop happened and could not keep
	// them, and always empty outside Linux.
	LostCaps []string
}

// Apply makes the data directory belong to the user the daemon will run as, and
// then becomes that user.
//
// Call it once the configuration is loaded, so the paths are known, and before
// the store is opened or any socket exists. There is no way back afterwards: a
// privileged setuid sets the saved uid as well, so no code that runs later can
// return to root, including this package's.
func Apply(opts Options) (Report, error) {
	return apply(opts, os.Getenv, os.Geteuid(), applyRoot)
}

// apply is the decision, separated from the syscalls so that each branch can be
// tested on a laptop that is not root — and so that no test can accidentally
// drop the privileges of the test process itself, which would be the worst
// possible way to find out that a change broke something.
func apply(opts Options, getenv func(string) string, euid int, root func(Options, Settings) (Report, error)) (Report, error) {
	settings, err := ParseSettings(getenv)

	// A malformed value only stops the start when something was going to be done
	// with it. Refusing to start over a variable the process could not have acted
	// on anyway would be theatre.
	if err != nil && euid == 0 {
		return Report{}, err
	}

	if euid != 0 {
		// The common case by a wide margin: a binary run straight on a host, a
		// container started with --user, a rootless runtime. There is no root to
		// give up and no directory to take over, so this costs an environment
		// lookup and returns.
		rep := Report{UID: euid, GID: os.Getegid(), Reason: "not root"}
		switch {
		case err != nil:
			warn(opts.Logger, "PUID, PGID or UMASK cannot be understood, and this process is not root anyway",
				"error", err, "uid", euid)
		case anyIdentityEnv(getenv):
			warn(opts.Logger, "PUID, PGID and UMASK are set, but this process is not root: it cannot change user,"+
				" so the values are ignored. Start the container as root — the image's default — for them to apply.",
				"uid", euid)
		}
		return rep, nil
	}

	if settings.UID == 0 && settings.GID == 0 {
		// The way out of all of this, for anyone who wants the container to stay
		// root: say so, and it does nothing at all. Loud enough to be visible in
		// the log, because a watchdog that never drops its privileges is worth
		// noticing on a machine somebody else may be reading the logs of.
		info(opts.Logger, "PUID and PGID are 0: staying root, as asked", "uid", 0, "umask", fmt.Sprintf("%04o", settings.Umask))
		return Report{Reason: "PUID and PGID are 0, so root is kept", UID: 0, GID: 0}, nil
	}

	return root(opts, settings)
}

// anyIdentityEnv reports whether any of the three is set at all, which is what
// decides between saying nothing and saying that a setting went unused.
func anyIdentityEnv(getenv func(string) string) bool {
	for _, key := range []string{envUID, envGID, envUmask} {
		if strings.TrimSpace(getenv(key)) != "" {
			return true
		}
	}
	return false
}

// chownPaths is every path the preflight hands to the container user: the data
// directory and everything inside it, plus the database and its write-ahead-log
// siblings when SQLite keeps them somewhere else.
//
// The database normally sits in the data directory and is covered by the walk.
// When it does not, it gets its own entries rather than its directory getting a
// recursive one: that directory belongs to something else, and a database pointed
// at another volume is not a reason to hand the whole volume over.
func chownPaths(dataDir, dbPath string) ([]string, error) {
	paths, err := walkPaths(dataDir)
	if err != nil {
		return nil, err
	}
	if dbPath != "" && filepath.Dir(dbPath) != dataDir {
		paths = append(paths, dbPath, dbPath+"-wal", dbPath+"-shm")
	}
	return paths, nil
}

// walkPaths lists a directory and everything under it, without following
// symlinks.
//
// A symlink at the root is resolved first, because a /data that is itself a link
// (a platform pointing one name at another) would otherwise be reported as the
// link and not descended into — and it is the real directory that has to change
// owner.
func walkPaths(root string) ([]string, error) {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}

	var paths []string
	err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// warn and info log through the configured logger, or do nothing when there is
// none. Every caller in the program passes one; the tests do not.
func warn(l *slog.Logger, msg string, args ...any) {
	if l != nil {
		l.Warn(msg, args...)
	}
}

func info(l *slog.Logger, msg string, args ...any) {
	if l != nil {
		l.Info(msg, args...)
	}
}
