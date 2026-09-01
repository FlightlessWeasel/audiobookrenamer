//go:build !windows

package organize

import (
	"errors"
	"syscall"
)

// isCrossDevice reports whether err is a rename failure caused by src and dst
// living on different filesystems.
func isCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

// MaxPathLen is the longest absolute target path the planner will produce.
// Linux caps a full path at PATH_MAX (4096) and macOS at 1024; the lower of the
// two is used so a library is portable between them, and so a plan built on one
// host stays valid if the volume is later mounted on the other.
const MaxPathLen = 1024
