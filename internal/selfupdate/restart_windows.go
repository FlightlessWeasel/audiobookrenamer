//go:build windows

package selfupdate

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Exec hands off to the freshly written binary. Windows has no execve(2) to
// replace the running image, so Exec spawns the new binary as a fully detached
// process and then exits so this PID is released.
//
// "Detached" here means: a new process group and no console
// (CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS), and none of this process's
// standard handles inherited — the child must not be tied to a console or pipe
// that goes away when this process exits. It is pure handoff: main has already
// run its graceful HTTP shutdown and deferred cleanup before calling Exec.
func (u *Updater) Exec() error {
	cmd := exec.Command(u.execPath, os.Args[1:]...)
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer devnull.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start replacement process: %w", err)
	}
	os.Exit(0)
	return nil // unreachable
}
