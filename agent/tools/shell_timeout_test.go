package tools

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestShellTimeoutKillsChildProcess verifies that when the shell tool
// times out, the child process spawned by `sh -c <cmd>` is also killed
// (i.e. the entire process group is terminated, not just the sh parent).
//
// Before the fix, exec.CommandContext only SIGKILLs the immediate sh
// process, letting the child (e.g. `sleep 120`) become orphaned and
// continue running indefinitely.
func TestShellTimeoutKillsChildProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process group killing test not applicable on Windows")
	}

	// Use a uniquely identifiable command so pgrep doesn't match other tests.
	marker := "hakka-timeout-test-" + time.Now().Format("150405.000")
	cmd := "sleep 120; echo " + marker

	// Run the shell tool with a very short timeout.
	out := run(t, Shell(nil).Handler, map[string]any{
		"cmd":             cmd,
		"timeout_seconds": 2,
	})

	// 1. Verify the tool reports a timeout.
	timedOut, ok := out["timed_out"].(bool)
	if !ok {
		t.Fatalf("expected timed_out field in result, got: %+v", out)
	}
	if !timedOut {
		t.Fatalf("expected timed_out=true, got false; result: %+v", out)
	}

	// 2. Give the OS a moment to reap orphaned processes.
	time.Sleep(500 * time.Millisecond)

	// 3. Check that no sleep process from our command is still running.
	// pgrep returns 0 if found, non-zero if not found.
	pgrep := exec.Command("pgrep", "-f", "sleep 120")
	output, err := pgrep.Output()

	// pgrep succeeded → a sleep process is still alive ⇒ BUG!
	if err == nil {
		t.Fatalf("BUG: child process (sleep 120) is still running after shell timeout!\n"+
			"Matching process(es):\n%s", string(output))
	}

	// pgrep failed with non-zero exit — good, no matching processes.
	// But double-check: the error should be an ExitError, not a "not found" binary issue.
	if _, ok := err.(*exec.ExitError); !ok {
		t.Logf("pgrep could not be executed (may be missing): %v — skipping process check", err)
	} else {
		t.Logf("OK: no stray sleep processes found after shell timeout")
	}

	// 4. Also verify the echo didn't run (the marker should not be in stdout).
	stdout := out["stdout"]
	if stdoutStr, ok := stdout.(string); ok && strings.Contains(stdoutStr, marker) {
		t.Fatalf("unexpected: the echo after sleep was reached — command should have been killed, not completed")
	}
}
