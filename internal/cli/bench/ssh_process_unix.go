//go:build unix

package bench

import (
	"os/exec"
	"syscall"
)

func isolateSSHProcess(process *exec.Cmd) {
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
