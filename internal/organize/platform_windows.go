//go:build windows

package organize

import (
	"errors"
	"syscall"
)

// errNotSameDevice is ERROR_NOT_SAME_DEVICE, what MoveFileEx reports for a
// rename whose source and destination are on different volumes. Windows does
// not surface EXDEV for this, so checking EXDEV alone would miss every
// cross-volume move.
const errNotSameDevice = syscall.Errno(0x11)

// isCrossDevice reports whether err is a rename failure caused by src and dst
// living on different filesystems.
func isCrossDevice(err error) bool {
	return errors.Is(err, errNotSameDevice) || errors.Is(err, syscall.EXDEV)
}

// MaxPathLen is the longest absolute target path the planner will produce.
//
// Windows' MAX_PATH is 260 including the terminating NUL, so 259 usable
// characters. Go's os package transparently applies the \\?\ extended-length
// prefix to absolute paths, so this app could itself create longer ones — but
// the point of organizing a library is that other software reads it, and
// Explorer, most media scanners, and Audiobookshelf all still break past
// MAX_PATH. Staying inside the classic limit is what makes the result usable.
const MaxPathLen = 259
