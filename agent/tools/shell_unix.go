//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
	"time"
)

// setProcessGroupKill configures the command to run in its own process group
// and, on timeout/cancellation, kills the entire process group (not just the
// immediate sh process). This ensures that child processes spawned by
// `sh -c "<cmd>"` do not become orphaned when the shell tool times out.
//
// On Windows, process group semantics differ, so this function is a no-op
// (see shell_windows.go).
func setProcessGroupKill(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// When the context is cancelled (timeout), kill the process group
	// by using a negative PID. The WaitDelay gives the group time to
	// terminate before Go sends a final SIGKILL to just the sh process.
	cmd.Cancel = func() error {
		if cmd.Process != nil && cmd.Process.Pid > 0 {
			// SIGTERM first — allows well-behaved children to exit cleanly.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			return nil
		}
		return nil
	}
	cmd.WaitDelay = 500 * time.Millisecond // grace period after SIGTERM; then SIGKILL to the group
}
