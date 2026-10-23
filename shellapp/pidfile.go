package shellapp

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

const (
	pidFileExtension     = ".pid"
	sessionFileExtension = ".session"
	managedFileExtension = ".managed"
	startFileExtension   = ".start"
)

var instanceFileExtensions = []string{pidFileExtension, sessionFileExtension, managedFileExtension, startFileExtension}

func (a *App) pidFilePrefix() string { return a.cfg.ID + "-" }

// matchPrefix reports which ID prefix a runtime file name carries, so
// instances started under a legacy ID are still found.
func (a *App) matchPrefix(name string) (string, bool) {
	for _, id := range append([]string{a.cfg.ID}, a.cfg.LegacyIDs...) {
		if strings.HasPrefix(name, id+"-") {
			return id + "-", true
		}
	}
	return "", false
}

func (a *App) pidFilePath() string {
	return filepath.Join(a.runtimeDir(), fmt.Sprintf("%s%d%s", a.pidFilePrefix(), os.Getpid(), pidFileExtension))
}

func (a *App) sessionFilePath() string {
	return filepath.Join(a.runtimeDir(), fmt.Sprintf("%s%d%s", a.pidFilePrefix(), os.Getpid(), sessionFileExtension))
}

func (a *App) managedFilePath() string {
	return filepath.Join(a.runtimeDir(), fmt.Sprintf("%s%d%s", a.pidFilePrefix(), os.Getpid(), managedFileExtension))
}

func (a *App) startFilePath() string {
	return filepath.Join(a.runtimeDir(), fmt.Sprintf("%s%d%s", a.pidFilePrefix(), os.Getpid(), startFileExtension))
}

func (a *App) writePIDFile(childPID int) error {
	// Start times let a later takeover tell these processes from reused PIDs.
	if parentStart, ok := procStartTime(os.Getpid()); ok {
		if childStart, ok := procStartTime(childPID); ok {
			if err := os.WriteFile(a.startFilePath(), []byte(fmt.Sprintf("%d %d", parentStart, childStart)), 0o644); err != nil {
				return err
			}
		}
	}
	if a.sessionManaged {
		if err := os.WriteFile(a.managedFilePath(), nil, 0o644); err != nil {
			return err
		}
	}
	if display := os.Getenv("WAYLAND_DISPLAY"); display != "" {
		if err := os.WriteFile(a.sessionFilePath(), []byte(display), 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(a.pidFilePath(), []byte(strconv.Itoa(childPID)), 0o644)
}

func (a *App) removePIDFile() {
	os.Remove(a.pidFilePath())
	os.Remove(a.sessionFilePath())
	os.Remove(a.managedFilePath())
	os.Remove(a.startFilePath())
}

func (a *App) removeAllPIDFiles() {
	entries, err := os.ReadDir(a.runtimeDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if _, ok := a.matchPrefix(name); !ok {
			continue
		}
		if slices.ContainsFunc(instanceFileExtensions, func(ext string) bool { return strings.HasSuffix(name, ext) }) {
			os.Remove(filepath.Join(a.runtimeDir(), name))
		}
	}
}

// PIDs returns the live UI child PIDs and their supervising parents,
// reaping PID files whose processes are gone.
func (a *App) PIDs() []int {
	dir := a.runtimeDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var pids []int

	for _, entry := range entries {
		name := entry.Name()
		prefix, ok := a.matchPrefix(name)
		if !ok || !strings.HasSuffix(name, pidFileExtension) {
			continue
		}

		pidFile := filepath.Join(dir, name)
		data, err := os.ReadFile(pidFile)
		if err != nil {
			continue
		}

		childPID, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || !processAlive(childPID) {
			removeInstanceFiles(strings.TrimSuffix(pidFile, pidFileExtension))
			continue
		}

		pids = append(pids, childPID)

		parentPIDStr := strings.TrimSuffix(strings.TrimPrefix(name, prefix), pidFileExtension)
		parentPID, err := strconv.Atoi(parentPIDStr)
		if err != nil {
			continue
		}
		if processAlive(parentPID) {
			pids = append(pids, parentPID)
		}
	}

	return pids
}

func removeInstanceFiles(base string) {
	for _, ext := range instanceFileExtensions {
		os.Remove(base + ext)
	}
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func (a *App) sessionParentPID(display string) (int, string, bool) {
	if display == "" {
		return 0, "", false
	}

	dir := a.runtimeDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, "", false
	}

	for _, entry := range entries {
		name := entry.Name()
		prefix, ok := a.matchPrefix(name)
		if !ok || !strings.HasSuffix(name, sessionFileExtension) {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || strings.TrimSpace(string(data)) != display {
			continue
		}

		parentStr := strings.TrimSuffix(strings.TrimPrefix(name, prefix), sessionFileExtension)
		parentPID, err := strconv.Atoi(parentStr)
		if err != nil {
			continue
		}

		// A leftover session file must not shadow a live instance listed after it.
		pidData, err := os.ReadFile(filepath.Join(dir, prefix+parentStr+pidFileExtension))
		if err != nil {
			continue
		}
		if uiPID, err := strconv.Atoi(strings.TrimSpace(string(pidData))); err != nil || !processAlive(uiPID) {
			continue
		}

		return parentPID, prefix, true
	}

	return 0, "", false
}

func (a *App) firstChildPID() (int, bool) {
	entries, err := os.ReadDir(a.runtimeDir())
	if err != nil {
		return 0, false
	}

	for _, entry := range entries {
		name := entry.Name()
		if _, ok := a.matchPrefix(name); !ok || !strings.HasSuffix(name, pidFileExtension) {
			continue
		}

		data, err := os.ReadFile(filepath.Join(a.runtimeDir(), name))
		if err != nil {
			continue
		}

		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || !processAlive(pid) {
			continue
		}

		return pid, true
	}

	return 0, false
}

// sessionUIPID returns the UI child PID for the current WAYLAND_DISPLAY
// session only, with no cross-session fallback.
func (a *App) sessionUIPID() (int, bool) {
	inst, ok := a.currentSessionInstance()
	return inst.ui.pid, ok
}

// SessionPID returns the UI child PID for the current WAYLAND_DISPLAY
// session, falling back to the first live instance.
func (a *App) SessionPID() (int, bool) {
	if pid, ok := a.sessionUIPID(); ok {
		return pid, true
	}
	return a.firstChildPID()
}

// SessionSocketPath returns the backend socket of the instance supervising
// the current WAYLAND_DISPLAY session.
func (a *App) SessionSocketPath() (string, bool) {
	parentPID, prefix, ok := a.sessionParentPID(os.Getenv("WAYLAND_DISPLAY"))
	if !ok {
		return "", false
	}

	socket := filepath.Join(a.runtimeDir(), fmt.Sprintf("%s%d.sock", prefix, parentPID))
	if _, err := os.Stat(socket); err != nil {
		return "", false
	}

	return socket, true
}
