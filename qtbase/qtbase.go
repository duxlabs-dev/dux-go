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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ebitengine/purego"
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

// Locate resolves the runtime library: the host's embedded copy, then
// Soname next to the executable, then ../lib/<executable name>/Soname,
// then the system library directories in loader order (LD_LIBRARY_PATH, ld.so.conf, /lib and /usr/lib).
func Locate(embedded Locator) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	return locate(embedded, exe, systemLibDirs(), fileExists)
}

func locate(embedded Locator, exe string, systemDirs []string, exists func(string) bool) (string, error) {
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
	for _, libDir := range systemDirs {
		candidates = append(candidates, filepath.Join(libDir, Soname))
	}
	for _, path := range candidates {
		if exists(path) {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s is not embedded in this build, not next to %s, and not in the system library directories: install dux-qt-base", Soname, exe)
}

func systemLibDirs() []string {
	var dirs []string
	for _, dir := range strings.Split(os.Getenv("LD_LIBRARY_PATH"), ":") {
		if dir != "" {
			dirs = append(dirs, dir)
		}
	}
	dirs = append(dirs, ldSoConfDirs("/etc/ld.so.conf", 0)...)
	return append(dirs, "/usr/lib64", "/usr/lib", "/lib64", "/lib", "/usr/local/lib64", "/usr/local/lib")
}

func ldSoConfDirs(conf string, depth int) []string {
	if depth > 4 {
		return nil
	}
	data, err := os.ReadFile(conf)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "include "):
			pattern := strings.TrimSpace(strings.TrimPrefix(line, "include "))
			if !filepath.IsAbs(pattern) {
				pattern = filepath.Join(filepath.Dir(conf), pattern)
			}
			matches, _ := filepath.Glob(pattern)
			for _, match := range matches {
				dirs = append(dirs, ldSoConfDirs(match, depth+1)...)
			}
		default:
			dirs = append(dirs, line)
		}
	}
	return dirs
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
	var dqbMain func(argc int32, argv **byte) int32
	purego.RegisterLibFunc(&dqbMain, handle, "dqb_main")

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
