//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package acpruntime

import (
	"os"
	"os/exec"

	acp "github.com/coder/acp-go-sdk"
)

func configureProcess(_ *exec.Cmd) {}

func stopProcess(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
}

func terminalProcessExitStatus(
	state *os.ProcessState,
	err error,
) acp.TerminalExitStatus {
	exitCode := -1
	if state != nil {
		exitCode = state.ExitCode()
	} else if err == nil {
		exitCode = 0
	}
	return acp.TerminalExitStatus{ExitCode: &exitCode}
}
