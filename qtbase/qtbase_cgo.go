//go:build cgo && !noqtbase

package qtbase

/*
#cgo pkg-config: dux-qt-base
#include <stdlib.h>
#include <dux-qt-base/dux_qt_base.h>
*/
import "C"

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"unsafe"
)

// Qt requires the main thread; init runs on it before the goroutine can migrate.
func init() { runtime.LockOSThread() }

func Embedded() bool { return true }

func Version() string { return C.GoString(C.dqb_version()) }

func Command(ctx context.Context, args ...string) *exec.Cmd {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	return exec.CommandContext(ctx, exe, append([]string{Subcommand}, args...)...)
}

func MaybeRunAndExit() {
	args, ok := runtimeArgs(os.Args)
	if !ok {
		return
	}
	os.Exit(run(args))
}

func run(args []string) int {
	argv := make([]*C.char, len(args)+1)
	for i, arg := range args {
		argv[i] = C.CString(arg)
		defer C.free(unsafe.Pointer(argv[i]))
	}
	return int(C.dqb_main(C.int(len(args)), &argv[0]))
}
