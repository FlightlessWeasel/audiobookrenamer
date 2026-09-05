//go:build !windows

package selfupdate

import (
	"os"
	"syscall"
)

// Exec replaces the running process image with the freshly written binary via
// execve(2), keeping the same PID, args and environment. It is pure handoff:
// main has already run its graceful HTTP shutdown and deferred cleanup (DB
// close, worker drain) before calling Exec. On success it never returns.
func (u *Updater) Exec() error {
	return syscall.Exec(u.execPath, os.Args, os.Environ())
}
