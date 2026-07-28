//go:build unix

package subprocess

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the child in its own process group, so that
// cancellation can take the whole tree rather than just the process we started.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree ends the process group.
//
// A negative pid addresses the group: a script that forked — a Python wrapper,
// a shell pipeline — leaves orphans otherwise, and an orphan holding the pipe
// open is indistinguishable from a process that has not answered yet.
func killTree(cmd *exec.Cmd) {
	pid := cmd.Process.Pid

	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		// Fall back to the process itself; the group may already be gone.
		_ = cmd.Process.Kill()
	}
}

// checkExecutable reports whether the file may be run by this process.
func checkExecutable(path string, info os.FileInfo) error {
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("subprocess: %s is not executable", path)
	}

	return nil
}
