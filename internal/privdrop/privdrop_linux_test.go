//go:build linux

package privdrop

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
)

// chown is one lchown the fake kernel was asked to make.
type chown struct {
	path     string
	uid, gid int
}

// fake is a kernel that does nothing and writes down what it was asked to do, in
// order. The sequence cannot be run for real without being root and without
// changing the identity of the process running the tests, and the order is the
// part of it most worth pinning down.
type fake struct {
	calls []string

	want    caps // what getCaps reports
	kept    caps // what setCaps was handed
	umask   int
	chowned []chown

	euidAfter    int
	setuidStuck  bool // setuid "succeeds" and the uid does not move
	errSetgroups error
	errSetCaps   error
	errSetuid    error
}

func (f *fake) kernel() kernel {
	return kernel{
		euid: func() int {
			return f.euidAfter
		},
		mkdirAll: func(string, os.FileMode) error {
			f.calls = append(f.calls, "mkdir")
			return nil
		},
		lchown: func(p string, uid, gid int) error {
			f.calls = append(f.calls, "lchown")
			f.chowned = append(f.chowned, chown{p, uid, gid})
			return nil
		},
		umask: func(m int) int {
			f.calls = append(f.calls, "umask")
			f.umask = m
			return 0
		},
		setgroups: func([]int) error {
			f.calls = append(f.calls, "setgroups")
			return f.errSetgroups
		},
		setgid: func(int) error {
			f.calls = append(f.calls, "setgid")
			return nil
		},
		setuid: func(uid int) error {
			f.calls = append(f.calls, "setuid")
			if f.errSetuid != nil {
				return f.errSetuid
			}
			// setuid(2) sets every id at once, so a successful call is what makes the
			// verification below see the new uid. setuidStuck is the case the kernel
			// does not have: it reports success and the uid stays where it was.
			if !f.setuidStuck {
				f.euidAfter = uid
			}
			return nil
		},
		getCaps: func() (caps, error) {
			f.calls = append(f.calls, "getcaps")
			return f.want, nil
		},
		keepCaps: func(on bool) error {
			f.calls = append(f.calls, fmt.Sprintf("keepcaps=%v", on))
			return nil
		},
		setCaps: func(c caps) error {
			f.calls = append(f.calls, "setcaps")
			f.kept = c
			return f.errSetCaps
		},
	}
}

// capsWithRaw is a container started with one capability: NET_RAW is bit 13, and
// holding it in both the permitted and the effective set is what --cap-add leaves
// behind for a root process.
func capsWithRaw() caps {
	c := caps{hdr: capHeader{version: linuxCapabilityVersion3}}
	c.data[0].permitted = 1 << 13
	c.data[0].effective = 1 << 13
	c.data[0].inheritable = 1 << 13
	return c
}

// TestApplyToOrder is the guard on the one thing that cannot be seen by reading a
// single line of the code: that ownership is taken before root is given up, that
// the capabilities are read before the change removes them, and that the group goes
// before the user.
func TestApplyToOrder(t *testing.T) {
	dir := t.TempDir()
	f := &fake{want: capsWithRaw()}

	rep, err := applyTo(Options{DataDir: dir}, Settings{UID: 1000, GID: 1001, Umask: 0o022}, f.kernel())
	if err != nil {
		t.Fatalf("applyTo: %v", err)
	}
	if !rep.Applied {
		t.Errorf("Report.Applied = false, want true: %+v", rep)
	}

	want := []string{"umask", "mkdir", "lchown", "getcaps", "keepcaps=true",
		"setgroups", "setgid", "setuid", "keepcaps=false", "setcaps"}
	if strings.Join(f.calls, " ") != strings.Join(want, " ") {
		t.Errorf("the sequence was\n  %v\nwant\n  %v", f.calls, want)
	}
	if f.umask != 0o022 {
		t.Errorf("umask = %04o, want 022", f.umask)
	}
	if len(f.chowned) != 1 || f.chowned[0].uid != 1000 || f.chowned[0].gid != 1001 {
		t.Errorf("chowned %v, want the data directory handed to 1000:1001", f.chowned)
	}
	if rep.UID != 1000 || rep.GID != 1001 {
		t.Errorf("Report says %d:%d, want the identity that was asked for", rep.UID, rep.GID)
	}
}

// TestApplyToKeepsTheCapabilities: the effective set is what a raw socket is
// opened with, and the kernel clears it when the uid becomes non-zero. What comes
// back has to be what the container was granted, or a ping sensor stops working
// with nothing anywhere saying why.
func TestApplyToKeepsTheCapabilities(t *testing.T) {
	f := &fake{want: capsWithRaw()}
	if _, err := applyTo(Options{DataDir: t.TempDir()}, Settings{UID: 500, GID: 500, Umask: 0o022}, f.kernel()); err != nil {
		t.Fatalf("applyTo: %v", err)
	}
	if got := f.kept.data[0].effective; got != 1<<13 {
		t.Errorf("effective = %#x, want NET_RAW back", got)
	}
	if got := f.kept.data[0].inheritable; got != 1<<13 {
		t.Errorf("inheritable = %#x, want what was there before", got)
	}
}

// TestApplyToWarnsWhenTheCapabilitiesAreLost: a watchdog that refuses to start
// because one sensor type lost a capability is the worse trade. It says so and
// carries on.
func TestApplyToWarnsWhenTheCapabilitiesAreLost(t *testing.T) {
	f := &fake{want: capsWithRaw(), errSetCaps: syscall.EPERM}
	rep, err := applyTo(Options{DataDir: t.TempDir()}, Settings{UID: 500, GID: 500, Umask: 0o022}, f.kernel())
	if err != nil {
		t.Fatalf("applyTo: %v, want the run to continue without the capability", err)
	}
	if !strings.Contains(strings.Join(rep.LostCaps, " "), "cap_net_raw") {
		t.Errorf("Report.LostCaps = %v, want the capability that was lost named", rep.LostCaps)
	}
}

// TestApplyToRefusesToStayRoot: setuid either changes every id or none of them, so
// finding ourselves still root afterwards means it did not happen. Refusing is the
// only safe reading of that.
func TestApplyToRefusesToStayRoot(t *testing.T) {
	f := &fake{want: capsWithRaw()}
	f.setuidStuck = true // the syscall "succeeds" and the uid does not move
	_, err := applyTo(Options{DataDir: t.TempDir()}, Settings{UID: 500, GID: 500, Umask: 0o022}, f.kernel())
	if err == nil || !strings.Contains(err.Error(), "refusing to run as root") {
		t.Fatalf("applyTo: %v, want a refusal to carry on as root", err)
	}
}

// TestApplyToStopsWhenTheUserCannotBeChanged: the failure to drop is fatal, and it
// names the namespace case, because that is what it usually is.
func TestApplyToStopsWhenTheUserCannotBeChanged(t *testing.T) {
	f := &fake{want: capsWithRaw(), errSetuid: syscall.EINVAL}
	_, err := applyTo(Options{DataDir: t.TempDir()}, Settings{UID: 1000, GID: 1000, Umask: 0o022}, f.kernel())
	if err == nil || !strings.Contains(err.Error(), "user namespace") {
		t.Fatalf("applyTo: %v, want a refusal that names the likely cause", err)
	}
}

// TestApplyToCarriesOnWithoutTheSupplementaryGroup: a user namespace that denies
// setgroups is not a reason to refuse to run. The primary group is what decides
// file access, and that one is set next.
func TestApplyToCarriesOnWithoutTheSupplementaryGroup(t *testing.T) {
	f := &fake{want: capsWithRaw(), errSetgroups: syscall.EPERM}
	rep, err := applyTo(Options{DataDir: t.TempDir()}, Settings{UID: 500, GID: 500, Umask: 0o022}, f.kernel())
	if err != nil {
		t.Fatalf("applyTo: %v, want it to continue with the primary group alone", err)
	}
	if !rep.Applied {
		t.Error("Report.Applied = false, want the drop to have happened")
	}
}

func TestApplyToRefusesADirectoryItCannotHandOver(t *testing.T) {
	f := &fake{want: capsWithRaw()}
	// The fake kernel reports that it created the directory; the walk then finds
	// that it is not there, which is what a read-only mount looks like.
	if _, err := applyTo(Options{DataDir: "/nonexistent/definitely/not/here"},
		Settings{UID: 500, GID: 500, Umask: 0o022}, f.kernel()); err == nil {
		t.Fatal("want an error for a data directory that cannot be listed")
	}
}

func TestCapsLostNamesWhatMatters(t *testing.T) {
	c := caps{}
	c.data[0].permitted = 1<<13 | 1<<0 | 1<<31
	c.data[1].permitted = 1 << 3 // capability 35, which has no name in the table
	got := strings.Join(c.lost(), " ")
	for _, want := range []string{"cap_net_raw", "cap_chown", "cap_setfcap", "cap_35"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost() = %q, want %s in it", got, want)
		}
	}
}
