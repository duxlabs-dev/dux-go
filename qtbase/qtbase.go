// Package qtbase runs the quickshell runtime from libdux-qt-base.so inside
// the host binary's own `qs` subcommand. The library is dlopen'd only there,
// so every other CLI call starts without mapping Qt, and the UI process still
// shows up as the host binary. No cgo: purego does the dlopen.
//
// Command re-executes the host with the Subcommand marker, which
// MaybeRunAndExit routes into the library before anything else starts.
package qtbase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/duxlabs-dev/dux-go/log"
	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

const (
	// Subcommand marks a re-exec of the host binary that should run the runtime.
	Subcommand   = "qs"
	Soname       = "libdux-qt-base.so.0"
	NativeBinary = "qs"
)

// Locator returns the path of a runtime library the host carries itself
// (an embedded copy it extracted), or "" when this build has none.
type Locator func() (string, error)

// Qt requires the main thread; init runs on it before the goroutine can migrate.
func init() {
	if _, ok := runtimeArgs(os.Args); ok {
		runtime.LockOSThread()
	}
}

func Command(ctx context.Context, args ...string) *exec.Cmd {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	return exec.CommandContext(ctx, exe, append([]string{Subcommand}, args...)...)
}

func MaybeRunAndExit(embedded Locator) {
	args, ok := runtimeArgs(os.Args)
	if !ok {
		return
	}
	lib, err := Locate(embedded)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(127)
	}
	os.Exit(run(lib, args))
}

func Locate(embedded Locator) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	return locate(embedded, exe, fileExists)
}

func locate(embedded Locator, exe string, exists func(string) bool) (string, error) {
	if embedded != nil {
		path, err := embedded()
		if err != nil {
			return "", err
		}
		if path != "" {
			return path, nil
		}
	}
	dir := filepath.Dir(exe)
	candidates := []string{
		filepath.Join(dir, Soname),
		filepath.Join(dir, "..", "lib", filepath.Base(exe), Soname),
	}
	for _, path := range candidates {
		if exists(path) {
			return path, nil
		}
	}
	return Soname, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// crashRelaunch is set when quickshell's crash handler re-execs
// /proc/self/exe with no arguments and state in the environment.
func crashRelaunch() bool {
	return os.Getenv("__QUICKSHELL_CRASH_INFO_FD") != "" || os.Getenv("__QUICKSHELL_CRASH_DUMP_FD") != ""
}

func runtimeArgs(args []string) ([]string, bool) {
	if len(args) > 1 && args[1] == Subcommand {
		return append([]string{Subcommand}, args[2:]...), true
	}
	if crashRelaunch() {
		return []string{Subcommand}, true
	}
	return nil, false
}

func run(lib string, args []string) int {
	handle, err := purego.Dlopen(lib, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load %s: %v\n", lib, err)
		return 127
	}
	entry, err := purego.Dlsym(handle, "dqb_main")
	if err != nil {
		fmt.Fprintf(os.Stderr, "load %s: %v\n", lib, err)
		return 127
	}
	path, err := loadedLibraryPath(entry)
	if err != nil {
		log.Warn("Could not resolve loaded runtime path", "library", lib, "err", err)
		path = lib
	}
	version := libraryString(handle, "dqb_version")
	revision := libraryString(handle, "dqb_git_revision")
	if err := publishRuntime(path, version, revision); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 127
	}
	log.Info("Loaded runtime library", "path", path, "version", version, "revision", revision)
	var dqbMain func(argc int32, argv **byte) int32
	purego.RegisterFunc(&dqbMain, entry)

	// QCoreApplication keeps the argv pointers for the whole run.
	argv := make([]*byte, len(args)+1)
	for i, arg := range args {
		cstr := append([]byte(arg), 0)
		argv[i] = &cstr[0]
	}
	code := dqbMain(int32(len(args)), &argv[0])
	runtime.KeepAlive(argv)
	return int(code)
}

type dlInfo struct {
	filename *byte
	_        [3]uintptr
}

func loadedLibraryPath(entry uintptr) (string, error) {
	symbol, err := purego.Dlsym(purego.RTLD_DEFAULT, "dladdr")
	if err != nil {
		return "", err
	}
	var dladdr func(uintptr, *dlInfo) int32
	purego.RegisterFunc(&dladdr, symbol)
	var info dlInfo
	if dladdr(entry, &info) == 0 || info.filename == nil {
		return "", errors.New("dladdr found no object containing dqb_main")
	}
	library, err := filepath.EvalSymlinks(unix.BytePtrToString(info.filename))
	if err != nil {
		return "", err
	}
	return filepath.Abs(library)
}

func libraryString(handle uintptr, name string) string {
	symbol, err := purego.Dlsym(handle, name)
	if err != nil {
		return ""
	}
	var read func() string
	purego.RegisterFunc(&read, symbol)
	return read()
}

func publishRuntime(library, version, revision string) error {
	for key, value := range map[string]string{
		"QTBASE_RUNTIME_LIBRARY":  library,
		"QTBASE_RUNTIME_VERSION":  version,
		"QTBASE_RUNTIME_REVISION": revision,
		"QTBASE_RUNTIME_PID":      strconv.Itoa(os.Getpid()),
	} {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}
