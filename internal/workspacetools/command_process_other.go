//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package workspacetools

import "os/exec"

func configureCommandProcess(_ *exec.Cmd) {}

func cleanupCommandProcess(_ *exec.Cmd) {}
