//go:build linux

package privdrop

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// applyRoot is the privileged sequence, in the one order that works.
//
// It is separate from applyTo only so that applyTo can be driven by a fake
// kernel in the tests: none of this can be exercised on a machine that is not
// root, and the order of the calls is the part most worth guarding.
func applyRoot(opts Options, s Settings) (Report, error) {
	return applyTo(opts, s, newKernel())
}

// kernel is every syscall this package makes, as a value, so that the sequence
// can be tested without being root and without changing the identity of the
// process running the tests.
type kernel struct {
	euid      func() int
	mkdirAll  func(string, os.FileMode) error
	lchown    func(string, int, int) error
	umask     func(int) int
	setgroups func([]int) error
	setgid    func(int) error
	setuid    func(int) error
	getCaps   func() (caps, error)
	keepCaps  func(bool) error
	setCaps   func(caps) error
}

func newKernel() kernel {
	return kernel{
		euid:      os.Geteuid,
		mkdirAll:  os.MkdirAll,
		lchown:    os.Lchown,
		umask:     syscall.Umask,
		setgroups: syscall.Setgroups,
		setgid:    syscall.Setgid,
		setuid:    syscall.Setuid,
		getCaps:   getCaps,
		keepCaps:  keepCaps,
		setCaps:   setCaps,
	}
}

// namespaceHint is appended when the user change fails. Inside a user namespace —
// rootless podman, a Kubernetes pod with runAsUser — the effective uid can be 0
// while the ids PUID and PGID ask for are not mapped, and no amount of privilege
// inside the namespace can invent them. Naming that turns a baffling failure into
// a one-line answer.
const namespaceHint = " (inside a user namespace the id asked for may simply not be mapped;" +
	" use an id the namespace has, or start the container as that user instead)"

func applyTo(opts Options, s Settings, k kernel) (Report, error) {
	rep := Report{UID: s.UID, GID: s.GID}

	// The mask first, because everything created from here on is created under it:
	// the directory below, the database SQLite writes a moment later, its
	// write-ahead log. umask(2) is per-process, so the one call covers every thread.
	k.umask(s.Umask)

	if err := k.mkdirAll(opts.DataDir, 0o750); err != nil {
		return rep, fmt.Errorf("create data directory %s: %w", opts.DataDir, err)
	}

	// Take ownership while this is still possible — a few lines further down,
	// nothing running here could.
	paths, err := chownPaths(opts.DataDir, opts.DBPath)
	if err != nil {
		return rep, fmt.Errorf("list the data directory %s: %w", opts.DataDir, err)
	}
	for i, path := range paths {
		if err := k.lchown(path, s.UID, s.GID); err != nil {
			if os.IsNotExist(err) {
				// The host side of a bind mount is allowed to have a life of its
				// own; something being removed between the walk and the chown is
				// not a reason to refuse to start.
				continue
			}
			// The directory itself is the one that has to work, so that one is
			// fatal. A file inside it that could not be handed over is worth a line
			// and no more: SQLite names the file it cannot open in its own error,
			// which is the more useful of the two.
			if i == 0 { // paths[0] is the data directory: walkPaths lists its root first
				return rep, fmt.Errorf("hand %s to uid %d: %w", path, s.UID, err)
			}
			warn(opts.Logger, "could not hand a file to the container user", "path", path, "error", err)
		}
	}

	// What the container was granted, read before the change takes it away. A
	// container started with --cap-add=NET_RAW holds it now in the effective set,
	// which is what a raw socket is opened with, and in the permitted set, which is
	// the one the kernel is about to keep for us.
	want, err := k.getCaps()
	if err != nil {
		return rep, fmt.Errorf("read the capabilities this container was started with: %w", err)
	}

	if err := k.keepCaps(true); err != nil {
		return rep, fmt.Errorf("ask the kernel to keep the capabilities across the user change: %w", err)
	}

	// Give up root. This order is forced: CAP_SETGID is gone the instant setuid
	// runs. The supplementary groups go first of all, because root's are exactly
	// the ones that would otherwise survive as a way back in.
	if err := k.setgroups([]int{s.GID}); err != nil {
		warn(opts.Logger, "could not set the supplementary group; continuing with the primary group alone",
			"error", err, "gid", s.GID)
	}
	if err := k.setgid(s.GID); err != nil {
		return rep, fmt.Errorf("become gid %d: %w%s", s.GID, err, namespaceHint)
	}
	if err := k.setuid(s.UID); err != nil {
		return rep, fmt.Errorf("become uid %d: %w%s", s.UID, err, namespaceHint)
	}
	// The permitted set is kept from here on without the flag. Nothing can exec
	// anyway, so leaving the bit set would serve no one.
	_ = k.keepCaps(false)

	// setuid(2) sets the real, effective and saved ids together or not at all, so
	// there is either nobody left who could be root or somebody who still is. Still
	// root here means the drop silently did not happen, which is the one outcome
	// this package exists to prevent.
	if got := k.euid(); got != s.UID {
		return rep, fmt.Errorf("still uid %d after asking to become uid %d: refusing to run as root", got, s.UID)
	}

	// The uid is non-zero now and the kernel has cleared the effective set, keeping
	// the permitted one because it was asked to. Put back what the container was
	// started with.
	if err := k.setCaps(want.keep()); err != nil {
		rep.LostCaps = want.lost()
		warn(opts.Logger, "the capabilities this container was started with could not be restored after the user change;"+
			" a ping sensor will report that no ICMP socket can be opened, every other sensor type is unaffected",
			"error", err, "lost", strings.Join(rep.LostCaps, ","))
		// Deliberately not fatal. Losing a capability costs one sensor type, which
		// says so in its own result; refusing to start at all would cost the whole
		// watchdog, and would do it on an installation that is otherwise fine.
		return rep, nil
	}

	rep.Applied = true
	rep.Reason = "took ownership of the data directory and became PUID:PGID"
	info(opts.Logger, "the data directory now belongs to the container user",
		"path", opts.DataDir, "uid", s.UID, "gid", s.GID, "umask", fmt.Sprintf("%04o", s.Umask))
	return rep, nil
}

// ---------------------------------------------------------------------------
// capabilities
// ---------------------------------------------------------------------------

// The layout is linux/capability.h's: a header naming the version, then two words
// — one for capabilities 0..31, one for 32..63 — each carrying the effective,
// permitted and inheritable bits. It is copied from the standard library's own
// copy of it in syscall/exec_linux.go rather than written from memory, because one
// field in the wrong place means the wrong capability gets set and nothing says so.
type capHeader struct {
	version uint32
	// pid is an input field the kernel reads through the pointer below, not
	// something it reports back, and 0 means "the calling thread" — which is the
	// thread each call is about, the single-threaded read here and the per-thread
	// write further down alike. Left at its zero value deliberately, as the standard
	// library leaves it; the directive is only there because the analyser cannot see
	// a field the kernel reads rather than this program.
	//
	//nolint:unused // read by the kernel through capPtr, never by this program
	pid int32
}

type capData struct {
	effective   uint32
	permitted   uint32
	inheritable uint32
}

type caps struct {
	hdr  capHeader
	data [2]capData
}

// linuxCapabilityVersion3 is _LINUX_CAPABILITY_VERSION_3, the version that
// describes two words and therefore every capability that exists.
const linuxCapabilityVersion3 = 0x20080522

// capPtr takes the address of one of the structs above for the kernel.
//
// The lint wants a reason for the conversion, and there is one: this is a
// fixed-layout kernel ABI, the pointer is valid only for the duration of the call
// it is passed to, and the go:uintptrescapes on the syscall declarations is what
// keeps the struct alive across it. It is in one place so the reason is written
// once instead of at every call site.
func capPtr[T any](p *T) uintptr {
	return uintptr(unsafe.Pointer(p)) //nolint:gosec // G103: fixed-layout kernel ABI, see above
}

// getCaps reads the capability sets.
//
// A plain single-thread syscall is right here, and would be wrong for the writes
// below: nothing has changed yet, so every thread holds the same sets, and reading
// one of them answers for all of them.
func getCaps() (caps, error) {
	c := caps{hdr: capHeader{version: linuxCapabilityVersion3}}
	if _, _, errno := syscall.Syscall6(syscall.SYS_CAPGET,
		capPtr(&c.hdr), capPtr(&c.data[0]), 0, 0, 0, 0); errno != 0 {
		return caps{}, errno
	}
	return c, nil
}

// keep returns the set to install after the user change: the one the container was
// started with.
//
// The kernel clears the effective set when the uid becomes non-zero and keeps the
// permitted one, which is what the KEEPCAPS flag is for, so putting the effective
// set back to permitted restores exactly what was there. The inheritable set is
// untouched by the uid change and is carried through unchanged.
func (c caps) keep() caps {
	out := c
	for i := range out.data {
		out.data[i].effective = c.data[i].permitted
	}
	return out
}

// lost names the capabilities in the permitted set, so the one warning a failure
// produces can say which sensor stops working rather than leaving a number to be
// looked up. Only the ones a container plausibly carries are named; anything else
// is printed as its number rather than guessed at.
func (c caps) lost() []string {
	var out []string
	for i, word := range c.data {
		for bit := 0; bit < 32; bit++ {
			if word.permitted&(1<<uint(bit)) == 0 {
				continue
			}
			n := uint(i*32 + bit)
			if name, ok := capNames[n]; ok {
				out = append(out, name)
				continue
			}
			out = append(out, fmt.Sprintf("cap_%d", n))
		}
	}
	return out
}

// setCaps installs a capability set on every OS thread.
//
// Capabilities are per-thread state in the kernel, so a capset on the calling
// thread alone would leave whichever thread a ping probe happens to land on
// without its socket — an intermittent failure that depends on the scheduler,
// which is the worst kind of bug report to be handed. AllThreadsSyscall is the
// same mechanism syscall.Setuid uses, and for the same reason; it stops the world,
// runs the call in each thread, and lets threads cloned afterwards inherit the
// result. It requires every thread to produce the same return value, which capset
// does: it either accepts the fixed struct or fails identically everywhere.
func setCaps(c caps) error {
	c.hdr.version = linuxCapabilityVersion3
	if _, _, errno := syscall.AllThreadsSyscall6(syscall.SYS_CAPSET,
		capPtr(&c.hdr), capPtr(&c.data[0]), 0, 0, 0, 0); errno != 0 {
		return errno
	}
	return nil
}

// keepCaps sets the flag that keeps the permitted capability set across a change
// to a non-zero uid. Securebits are per-thread as well, so this goes through the
// same mechanism: a thread that missed it loses its capabilities for good.
func keepCaps(on bool) error {
	var v uintptr
	if on {
		v = 1
	}
	if _, _, errno := syscall.AllThreadsSyscall(syscall.SYS_PRCTL,
		syscall.PR_SET_KEEPCAPS, v, 0); errno != 0 {
		return errno
	}
	return nil
}

// capNames is the kernel's spelling of the capabilities a container can be holding
// by default, plus the handful that get added on purpose, which is where NET_RAW
// comes from.
var capNames = map[uint]string{
	0:  "cap_chown",
	1:  "cap_dac_override",
	2:  "cap_dac_read_search",
	3:  "cap_fowner",
	4:  "cap_fsetid",
	5:  "cap_kill",
	6:  "cap_setgid",
	7:  "cap_setuid",
	8:  "cap_setpcap",
	10: "cap_net_bind_service",
	12: "cap_net_admin",
	13: "cap_net_raw",
	18: "cap_sys_chroot",
	19: "cap_sys_ptrace",
	21: "cap_sys_admin",
	27: "cap_mknod",
	29: "cap_audit_write",
	31: "cap_setfcap",
}
