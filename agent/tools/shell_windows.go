//go:build windows

package tools

import "os/exec"

// setProcessGroupKill is a no-op on Windows because Windows does not
// support Unix-style process groups. The default exec.CommandContext
// behaviour (killing only the immediate cmd.exe / sh process) will
// apply. Child processes may survive a timeout on Windows.
func setProcessGroupKill(cmd *exec.Cmd) {
	// No-op: Windows process group semantics differ.
}
