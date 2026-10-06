package cmd

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command into its own process group so that the
// whole group (including child processes) can be signalled together.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

// killProcessGroup sends SIGKILL to the process group led by pid (negative pid
// addresses the whole group).
func killProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
