//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package acpruntime

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	acp "github.com/coder/acp-go-sdk"
)

func configureProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
}

func stopProcess(command *exec.Cmd) {
	if command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
}

func terminalProcessExitStatus(
	state *os.ProcessState,
	err error,
) acp.TerminalExitStatus {
	if state != nil {
		if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			signal := status.Signal().String()
			return acp.TerminalExitStatus{Signal: &signal}
		}
		exitCode := state.ExitCode()
		return acp.TerminalExitStatus{ExitCode: &exitCode}
	}
	exitCode := -1
	if err == nil {
		exitCode = 0
	}
	return acp.TerminalExitStatus{ExitCode: &exitCode}
}
