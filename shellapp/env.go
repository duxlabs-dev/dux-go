package shellapp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/duxlabs-dev/dux-go/log"
	"github.com/duxlabs-dev/dux-go/qtbase"
)

// uiWaitDelay bounds Wait after qs exits: processes qs forks (PAM
// conversations) inherit its stderr pipe and can outlive it.
const uiWaitDelay = 2 * time.Second

func hasSystemdRun() bool {
	_, err := exec.LookPath("systemd-run")
	return err == nil
}

func (a *App) appendLogEnv(env []string) []string {
	for _, key := range []string{a.cfg.EnvPrefix + "_LOG_LEVEL", a.cfg.EnvPrefix + "_LOG_FILE"} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	if rules := log.GetQtLoggingRules(); rules != "" {
		env = append(env, "QT_LOGGING_RULES="+rules)
	}
	return env
}

func appendQtEnv(env []string) []string {
	if os.Getenv("QT_QPA_PLATFORMTHEME") == "" {
		env = append(env, "QT_QPA_PLATFORMTHEME=gtk3")
	}
	if os.Getenv("QT_QPA_PLATFORMTHEME_QT6") == "" {
		env = append(env, "QT_QPA_PLATFORMTHEME_QT6=gtk3")
	}
	if os.Getenv("QT_QPA_PLATFORM") == "" {
		env = append(env, "QT_QPA_PLATFORM=wayland;xcb")
	}
	return env
}

func (a *App) qsChildEnv() string { return a.cfg.EnvPrefix + "_QS_CHILD" }

func (a *App) qsChild() bool { return os.Getenv(a.qsChildEnv()) != "" }

func (a *App) checkUIRuntime() error {
	if a.qsChild() {
		if _, err := exec.LookPath(qtbase.NativeBinary); err != nil {
			return fmt.Errorf("%s is set but %s is not on PATH", a.qsChildEnv(), qtbase.NativeBinary)
		}
		return nil
	}
	if _, err := qtbase.Locate(a.cfg.QtBase); err != nil {
		return fmt.Errorf("%w\nto run the UI on upstream quickshell instead, use `%s run --qs-child`", err, binaryName())
	}
	return nil
}

// Upstream quickshell runs the same QML; fork-only pragmas and properties degrade to warnings there.
func (a *App) uiCommand(ctx context.Context) *exec.Cmd {
	if a.qsChild() {
		return exec.CommandContext(ctx, qtbase.NativeBinary, "-p", a.configPath)
	}
	return qtbase.Command(ctx, "-p", a.configPath)
}

func (a *App) buildUICommand(ctx context.Context, socketPath string) *exec.Cmd {
	cmd := a.uiCommand(ctx)
	cmd.WaitDelay = uiWaitDelay
	env := append(os.Environ(),
		a.cfg.EnvPrefix+"_SOCKET="+socketPath,
		"QS_APP_ID="+a.cfg.QSAppID,
	)

	if a.startHidden {
		env = append(env, a.cfg.EnvPrefix+"_START_HIDDEN=1")
	}

	if a.sessionManaged && hasSystemdRun() {
		env = append(env, a.cfg.EnvPrefix+"_DEFAULT_LAUNCH_PREFIX=systemd-run --user --scope")
	}

	env = a.appendHotReloadEnv(env)
	env = appendQtEnv(env)
	env = a.appendLogEnv(env)
	if a.cfg.ExtraEnv != nil {
		env = append(env, a.cfg.ExtraEnv(a.configPath)...)
	}
	cmd.Env = env
	return cmd
}

func (a *App) appendHotReloadEnv(env []string) []string {
	key := a.cfg.EnvPrefix + "_DISABLE_HOT_RELOAD"
	if os.Getenv(key) != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.HasPrefix(a.configPath, home) {
		return env
	}
	return append(env, key+"=1")
}
