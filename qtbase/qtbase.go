// Package qtbase runs the quickshell command line from the dux-qt-base
// library linked into this binary. The UI still runs as its own process:
// Command re-executes this binary with the Subcommand marker, which
// MaybeRunAndExit routes into the library before anything else starts.
//
// Builds without cgo (or with the noqtbase tag) fall back to a qs binary on
// PATH, which keeps CGO_ENABLED=0 release artifacts working.
package qtbase

import "os"

// Subcommand marks a re-exec of this binary that should run the runtime.
const Subcommand = "qs"

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
