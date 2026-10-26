//go:build !unix

package bench

import "os/exec"

func isolateSSHProcess(_ *exec.Cmd) {}
