//go:build !cgo || noqtbase

package qtbase

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

const qsBinary = "qs"

func Embedded() bool { return false }

func Version() string { return "" }

func Command(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, qsBinary, args...)
}

func MaybeRunAndExit() {
	args, ok := runtimeArgs(os.Args)
	if !ok {
		return
	}
	path, err := exec.LookPath(qsBinary)
	if err != nil {
		os.Stderr.WriteString("qs not found on PATH and this build has no embedded runtime\n")
		os.Exit(127)
	}
	if err := syscall.Exec(path, append([]string{qsBinary}, args[1:]...), os.Environ()); err != nil {
		os.Stderr.WriteString("exec qs: " + err.Error() + "\n")
		os.Exit(126)
	}
}
