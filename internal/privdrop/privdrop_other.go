//go:build !linux

package privdrop

import (
	"fmt"
	"runtime"
)

// applyRoot has nothing to do on a platform that is not Linux: changing user is a
// Linux facility, and this package's job is done by the kernel there.
//
// Reaching this means the binary was started as root somewhere else — sudo on a
// laptop, most likely — and carrying on as root is the one outcome this package
// exists to prevent. So it refuses, and says how to say that root was meant.
func applyRoot(_ Options, _ Settings) (Report, error) {
	return Report{}, fmt.Errorf("started as root, but changing user is only implemented on Linux, and this is %s:"+
		" run it as yourself, or set PUID=0 to say that root is intended", runtime.GOOS)
}
