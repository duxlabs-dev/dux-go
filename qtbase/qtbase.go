// Package qtbase locates the quickshell runtime executable built by
// dux-qt-base. The host binary stays pure Go, so a plain CLI call never maps
// the Qt stack; only the UI process pays for it.
//
// The runtime is looked up next to the host executable, then on PATH.
package qtbase

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

const Binary = "dux-qt-base"

// Path returns the resolved runtime executable, cached for the process.
var Path = sync.OnceValues(func() (string, error) {
	return resolve(executableDir(), exec.LookPath)
})

func Command(ctx context.Context, args ...string) *exec.Cmd {
	path, err := Path()
	if err != nil {
		cmd := exec.CommandContext(ctx, Binary, args...)
		cmd.Err = err
		return cmd
	}
	return exec.CommandContext(ctx, path, args...)
}

func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

func resolve(exeDir string, lookPath func(string) (string, error)) (string, error) {
	if exeDir != "" {
		if path, err := lookPath(filepath.Join(exeDir, Binary)); err == nil {
			return path, nil
		}
	}
	if path, err := lookPath(Binary); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("%s not found next to this executable or on PATH", Binary)
}
