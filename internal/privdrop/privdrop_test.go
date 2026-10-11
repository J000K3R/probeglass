package privdrop

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

// resolve is t.TempDir with the symlinks taken out of it, which is what the walk
// reports: it resolves the root before descending, so on a platform whose temporary
// directory is reached through a link — /var is one on macOS — the paths it returns are
// not spelled the way t.TempDir spells them. Comparing the resolved forms is what makes
// the test say the same thing everywhere.
func resolve(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", path, err)
	}
	return resolved
}

func TestParseSettings(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		want      Settings
		wantError bool
	}{
		{name: "nothing set", want: Settings{UID: 500, GID: 500, Umask: 0o022}},
		{name: "the defaults written out", env: map[string]string{"PUID": "500", "PGID": "500", "UMASK": "022"},
			want: Settings{UID: 500, GID: 500, Umask: 0o022}},
		{name: "another id", env: map[string]string{"PUID": "1000", "PGID": "1001"},
			want: Settings{UID: 1000, GID: 1001, Umask: 0o022}},
		{name: "stay root", env: map[string]string{"PUID": "0", "PGID": "0"},
			want: Settings{UID: 0, GID: 0, Umask: 0o022}},
		{name: "spaces around the value", env: map[string]string{"PUID": " 500 "},
			want: Settings{UID: 500, GID: 500, Umask: 0o022}},

		// A mask is octal wherever it is written down, and all three spellings of
		// the same one have to read the same.
		{name: "mask without a leading zero", env: map[string]string{"UMASK": "22"},
			want: Settings{UID: 500, GID: 500, Umask: 0o022}},
		{name: "mask in the long form", env: map[string]string{"UMASK": "0022"},
			want: Settings{UID: 500, GID: 500, Umask: 0o022}},
		{name: "widest mask", env: map[string]string{"UMASK": "777"},
			want: Settings{UID: 500, GID: 500, Umask: 0o777}},

		{name: "uid that is not a number", env: map[string]string{"PUID": "abc"}, wantError: true},
		{name: "uid below zero", env: map[string]string{"PGID": "-1"}, wantError: true},
		{name: "uid too large", env: map[string]string{"PUID": "4294967296"}, wantError: true},
		{name: "mask that is not octal", env: map[string]string{"UMASK": "888"}, wantError: true},
		{name: "mask above 777", env: map[string]string{"UMASK": "1000"}, wantError: true},

		// The one that matters: a compose file turns an unquoted 022 into the
		// number 18, and 0018 is not a mask anybody typed. Failing loudly is the
		// only way that typo gets noticed.
		{name: "mask that lost its leading zero to yaml", env: map[string]string{"UMASK": "18"}, wantError: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSettings(env(tc.env))
			if tc.wantError {
				if err == nil {
					t.Fatalf("ParseSettings(%v) = %+v, want an error", tc.env, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSettings(%v): %v", tc.env, err)
			}
			if got != tc.want {
				t.Errorf("ParseSettings(%v) = %+v, want %+v", tc.env, got, tc.want)
			}
		})
	}
}

// TestApplyDecidesBeforeItTouchesAnything walks the branches that lead up to the
// syscalls. None of them may run on a process that is not root, and none of them
// may run at all once the decision has been made to stay root.
func TestApplyDecidesBeforeItTouchesAnything(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		euid     int
		wantDrop bool
		wantErr  bool
	}{
		{name: "an ordinary process", euid: 1000},
		{name: "an ordinary process with the settings set", euid: 1000, env: map[string]string{"PUID": "1000", "UMASK": "077"}},
		{name: "an ordinary process with a typo", euid: 1000, env: map[string]string{"PUID": "abc"}},
		{name: "root, asked to stay root", euid: 0, env: map[string]string{"PUID": "0", "PGID": "0"}},
		{name: "root", euid: 0, wantDrop: true},
		{name: "root with nothing set", euid: 0, wantDrop: true},
		{name: "root with a typo", euid: 0, env: map[string]string{"PUID": "abc"}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dropped := false
			root := func(_ Options, s Settings) (Report, error) {
				dropped = true
				return Report{Applied: true, UID: s.UID, GID: s.GID}, nil
			}
			rep, err := apply(Options{Logger: slog.New(slog.DiscardHandler)}, env(tc.env), tc.euid, root)

			if tc.wantErr && err == nil {
				t.Fatal("want an error for a value that cannot be understood by a process that would use it")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("apply: %v", err)
			}
			if dropped != tc.wantDrop {
				t.Errorf("dropped = %v, want %v", dropped, tc.wantDrop)
			}
			if tc.wantDrop {
				return
			}
			if rep.Applied {
				t.Errorf("Report.Applied = true, want false when nothing was done")
			}
			if tc.euid != 0 && rep.UID != tc.euid {
				t.Errorf("Report.UID = %d, want the uid the process already had (%d)", rep.UID, tc.euid)
			}
		})
	}
}

// TestChownPaths covers what the preflight hands over: the data directory and
// everything under it, and the database with its siblings only when it lives
// somewhere else — never the directory around it, which belongs to something else.
func TestChownPaths(t *testing.T) {
	dir := resolve(t, t.TempDir())
	nested := filepath.Join(dir, "sub")
	if err := os.Mkdir(nested, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "weatherglass.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// The database in the data directory is covered by the walk, and the walk
	// descends, so one in a subdirectory of it is covered too.
	paths, err := chownPaths(dir, filepath.Join(dir, "weatherglass.db"))
	if err != nil {
		t.Fatalf("chownPaths: %v", err)
	}
	want := map[string]bool{
		dir:                                      true,
		nested:                                   true,
		filepath.Join(nested, "weatherglass.db"): true,
	}
	if len(paths) != len(want) {
		t.Fatalf("chownPaths listed %v, want exactly the directory and its contents", paths)
	}
	for _, p := range paths {
		if !want[p] {
			t.Errorf("chownPaths listed %s, which is not part of the data directory", p)
		}
	}

	// A database on another volume gets its own entries — including the
	// write-ahead log and shared-memory files SQLite creates next to it — and none
	// of the directory it happens to sit in.
	elsewhere := filepath.Join(resolve(t, t.TempDir()), "weatherglass.db")
	paths, err = chownPaths(dir, elsewhere)
	if err != nil {
		t.Fatalf("chownPaths: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range paths {
		seen[p] = true
	}
	for _, p := range []string{elsewhere, elsewhere + "-wal", elsewhere + "-shm"} {
		if !seen[p] {
			t.Errorf("chownPaths(%s) did not list %s", elsewhere, p)
		}
	}
	if seen[filepath.Dir(elsewhere)] {
		t.Errorf("chownPaths listed %s: the directory around a database elsewhere is not ours to hand over",
			filepath.Dir(elsewhere))
	}
}

// TestWalkPathsDoesNotFollowSymlinks is the security-relevant one. The data
// directory is a folder other things write to — a bind mount's host side, a
// restore, a second container — so a link planted in it must not be able to point
// the chown at a file outside, which would be a way for whoever can write there to
// hand /etc/shadow to uid 500.
func TestWalkPathsDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "target")
	if err := os.WriteFile(target, []byte("not ours\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideDir := filepath.Join(outside, "inner")
	if err := os.Mkdir(outsideDir, 0o750); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(outsideDir, "file")
	if err := os.WriteFile(inside, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	dir := resolve(t, t.TempDir())
	fileLink := filepath.Join(dir, "file-link")
	dirLink := filepath.Join(dir, "dir-link")
	if err := os.Symlink(target, fileLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(outsideDir, dirLink); err != nil {
		t.Fatal(err)
	}

	paths, err := walkPaths(dir)
	if err != nil {
		t.Fatalf("walkPaths: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range paths {
		seen[p] = true
		if p == target || p == inside {
			t.Fatalf("walkPaths reported %s, which is outside the data directory", p)
		}
	}
	for _, want := range []string{dir, fileLink, dirLink} {
		if !seen[want] {
			t.Errorf("walkPaths did not report %s", want)
		}
	}
}

func TestWalkPathsResolvesALinkedRoot(t *testing.T) {
	real := resolve(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(real, "weatherglass.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "data")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	paths, err := walkPaths(link)
	if err != nil {
		t.Fatalf("walkPaths: %v", err)
	}
	if len(paths) < 2 {
		t.Fatalf("walkPaths(%s) = %v, want the real directory and its contents", link, paths)
	}
	if paths[0] != real {
		t.Errorf("walkPaths(%s)[0] = %s, want the resolved %s: it is the real directory that changes owner", link, paths[0], real)
	}
}

func TestAnyIdentityEnv(t *testing.T) {
	tests := []struct {
		env  map[string]string
		want bool
	}{
		{nil, false},
		{map[string]string{"PUID": ""}, false},
		{map[string]string{"UMASK": " "}, false},
		{map[string]string{"PGID": "500"}, true},
		{map[string]string{"UMASK": "022"}, true},
	}
	for _, tc := range tests {
		if got := anyIdentityEnv(env(tc.env)); got != tc.want {
			t.Errorf("anyIdentityEnv(%v) = %v, want %v", tc.env, got, tc.want)
		}
	}
}
