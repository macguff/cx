//go:build darwin || linux

package cx

import (
	"io"
	"os/exec"
	"syscall"
)

type systemRunner struct{}

func (systemRunner) Run(path string, args []string, env []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func (systemRunner) Exec(path string, args []string, env []string) error {
	return syscall.Exec(path, args, env)
}
