// Package shellapp runs a quickshell-based desktop app: it supervises the
// UI process, daemonizes, tracks instances via PID files, resolves the
// shell config dir, and talks to a running instance over dankgo/ipc.
package shellapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/duxlabs-dev/dux-go/paths"
	"github.com/duxlabs-dev/dux-go/qtbase"
	"github.com/spf13/cobra"
)

type Backend interface {
	SocketPath() string
	Close()
}

type Embedded interface {
	Available() bool
	Extract(baseDir string) (string, error)
	Prune(baseDir, keep string)
}

type Config struct {
	ID         string   // xdg/socket/pidfile identity, e.g. "dankcal"
	LegacyIDs  []string // earlier IDs, still matched when finding a running instance
	EnvPrefix  string   // default: upper(ID); drives <P>_SOCKET, <P>_SHELL_DIR, <P>_LOG_*
	QSAppID    string   // e.g. "dev.duxlabs.calendar"
	Version    string
	Embedded   Embedded
	QtBase     qtbase.Locator // embedded libdux-qt-base, nil when the host carries none
	Boot       func(ctx context.Context) (Backend, error)
	PreLaunch  func()
	ExtraEnv   func(configPath string) []string
	OnUIExit   func(exitCode int, uptime time.Duration, stderrTail string)
	ShowMethod string // IPC method `run` calls on a live instance instead of relaunching; empty relaunches

	SessionRestartExitCode int                              // exit code on SIGUSR1 managed restart (defaults to 1)
	TryManagedRestart      func() (handled bool, err error) // optional hook to route restart via service manager
}

type App struct {
	cfg            Config
	app            paths.App
	customConfig   string
	configPath     string
	sessionManaged bool
	startHidden    bool
}

func New(cfg Config) *App {
	if cfg.EnvPrefix == "" {
		cfg.EnvPrefix = strings.ToUpper(cfg.ID)
	}
	return &App{cfg: cfg, app: paths.New(cfg.ID)}
}

func (a *App) CustomConfigVar() *string { return &a.customConfig }

func (a *App) ConfigPath() string { return a.configPath }

func (a *App) shellDirEnv() string { return a.cfg.EnvPrefix + "_SHELL_DIR" }

func (a *App) runtimeDir() string { return a.app.SocketDir() }

func (a *App) stateFile() string {
	return filepath.Join(a.runtimeDir(), a.cfg.ID+".path")
}

func binaryName() string { return filepath.Base(os.Args[0]) }

// ResolveConfig resolves the quickshell config dir. Explicit overrides win
// (custom path, then <PREFIX>_SHELL_DIR), then the path a running instance
// is using, then the UI embedded in the binary. There is no implicit
// filesystem search.
func (a *App) ResolveConfig(_ *cobra.Command, _ []string) error {
	if a.customConfig != "" {
		dir, err := resolveShellDir(a.customConfig)
		if err != nil {
			return fmt.Errorf("custom config path: %w", err)
		}
		a.configPath = dir
		return nil
	}

	if raw := os.Getenv(a.shellDirEnv()); raw != "" {
		dir, err := resolveShellDir(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", a.shellDirEnv(), err)
		}
		a.configPath = dir
		return nil
	}

	if data, err := os.ReadFile(a.stateFile()); err == nil {
		switch len(a.PIDs()) {
		case 0:
			os.Remove(a.stateFile())
		default:
			statePath := strings.TrimSpace(string(data))
			if err := validateShellDir(statePath); err == nil {
				a.configPath = statePath
				return nil
			}
			os.Remove(a.stateFile())
		}
	}

	return a.useEmbeddedShell()
}

// resolveShellDir normalizes a user-supplied dir to an absolute path before
// validating it, so downstream heuristics keyed on the path (e.g. the
// $HOME-prefix hot-reload check) see a stable form regardless of whether the
// caller passed it relative to the cwd.
func resolveShellDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := validateShellDir(abs); err != nil {
		return "", err
	}
	return abs, nil
}

func validateShellDir(dir string) error {
	shellPath := filepath.Join(dir, "shell.qml")
	info, err := os.Stat(shellPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("expected file, got directory: %s", shellPath)
	}
	return nil
}

func (a *App) useEmbeddedShell() error {
	if a.cfg.Embedded == nil || !a.cfg.Embedded.Available() {
		return fmt.Errorf("this %s build has no embedded UI; pass -c <dir> or set %s", binaryName(), a.shellDirEnv())
	}

	baseDir := filepath.Join(a.runtimeDir(), a.cfg.ID+"-shell")
	dir, err := a.cfg.Embedded.Extract(baseDir)
	if err != nil {
		return fmt.Errorf("extract embedded UI: %w", err)
	}

	if len(a.PIDs()) == 0 {
		a.cfg.Embedded.Prune(baseDir, dir)
	}

	a.configPath = dir
	return nil
}

func (a *App) writeConfigState() error {
	return os.WriteFile(a.stateFile(), []byte(a.configPath), 0o644)
}

func (a *App) clearConfigState() {
	os.Remove(a.stateFile())
}
