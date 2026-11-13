package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type App struct{ Name string }

func New(name string) App { return App{Name: name} }

func (a App) ConfigDir() (string, error) {
	return ensureDir(filepath.Join(XDGConfigHome(), a.Name), 0o755)
}

func (a App) DataDir() (string, error) {
	return ensureDir(filepath.Join(XDGDataHome(), a.Name), 0o700)
}

func (a App) CacheDir() (string, error) {
	return ensureDir(filepath.Join(XDGCacheHome(), a.Name), 0o755)
}

func (a App) StateDir() (string, error) {
	return ensureDir(filepath.Join(XDGStateHome(), a.Name), 0o755)
}

func (a App) SocketDir() string {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	// The per-app runtime dir is bind-mounted into the Flatpak sandbox at the
	// same path it has on the host, so sockets placed there are reachable from
	// both sides.
	if id := os.Getenv("FLATPAK_ID"); id != "" && runtime != "" {
		return filepath.Join(runtime, "app", id)
	}
	if runtime != "" {
		return runtime
	}
	if os.Getuid() == 0 {
		if _, err := os.Stat("/run"); err == nil {
			return filepath.Join("/run", a.Name)
		}
		return filepath.Join("/var/run", a.Name)
	}
	return os.TempDir()
}

func (a App) SocketPath() string {
	return filepath.Join(a.SocketDir(), fmt.Sprintf("%s-%d.sock", a.Name, os.Getpid()))
}

func ensureDir(dir string, perm os.FileMode) (string, error) {
	if err := os.MkdirAll(dir, perm); err != nil {
		return "", fmt.Errorf("create dir: %w", err)
	}
	return dir, nil
}

func XDGConfigHome() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

func XDGCacheHome() string {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache")
}

func XDGDataHome() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

func XDGStateHome() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state")
}

const (
	DefaultDataDirs   = "/usr/local/share:/usr/share"
	DefaultConfigDirs = "/etc/xdg"
)

// XDGDataDirs is $XDG_DATA_DIRS split, or the spec default when it is unset or empty.
func XDGDataDirs() []string {
	return SplitPathList(EnvOr(os.Getenv, "XDG_DATA_DIRS", DefaultDataDirs))
}

// XDGConfigDirs is $XDG_CONFIG_DIRS split, or the spec default when it is unset or empty.
func XDGConfigDirs() []string {
	return SplitPathList(EnvOr(os.Getenv, "XDG_CONFIG_DIRS", DefaultConfigDirs))
}

// EnvOr returns env(key), or fallback when that is empty.
func EnvOr(env func(string) string, key, fallback string) string {
	if v := env(key); v != "" {
		return v
	}
	return fallback
}

// SplitPathList splits a colon-separated list, dropping blank entries.
func SplitPathList(raw string) []string {
	var out []string
	for dir := range strings.SplitSeq(raw, ":") {
		if dir = strings.TrimSpace(dir); dir != "" {
			out = append(out, dir)
		}
	}
	return out
}

func ExpandPath(path string) (string, error) {
	expanded := os.ExpandEnv(path)

	if strings.HasPrefix(expanded, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		expanded = filepath.Join(home, expanded[1:])
	}

	return filepath.Clean(expanded), nil
}
