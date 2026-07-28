//go:build windows

package subprocess

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// configureProcessGroup gives the child its own process group, the Windows
// counterpart of setpgid: it is what lets the whole tree be addressed later.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killTree ends the process and everything it spawned.
//
// Windows has no kill-the-group call, so taskkill /T is what walks the tree.
// Killing only the process we started would leave a forked child holding the
// pipe open, and such an orphan is indistinguishable from a process that has
// not answered yet.
func killTree(cmd *exec.Cmd) {
	pid := cmd.Process.Pid

	//nolint:gosec,noctx // fixed command, pid formatted by us; it must run even while a call is being torn down
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	if err := kill.Run(); err != nil {
		// taskkill may be missing or the tree already gone; the process itself
		// is still ours to end.
		_ = cmd.Process.Kill()
	}
}

// runnableExtensions are the suffixes Windows will execute directly.
var runnableExtensions = []string{".exe", ".bat", ".cmd", ".com"}

// checkExecutable reports whether the file may be run by this process.
//
// The Unix permission bits carry no meaning here — Go reports 0666 or 0444 for
// every file on Windows, so testing them would reject every Grader. The
// extension is what actually decides whether the file can be launched.
func checkExecutable(path string, _ os.FileInfo) error {
	ext := strings.ToLower(filepath.Ext(path))
	if !slices.Contains(runnableExtensions, ext) {
		return fmt.Errorf("subprocess: %s is not executable on Windows; expected one of %s",
			path, strings.Join(runnableExtensions, ", "))
	}

	return nil
}
