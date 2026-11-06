package shellapp

import (
	"context"
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

func (a *App) buildUICommand(ctx context.Context, socketPath string) *exec.Cmd {
	cmd := qtbase.Command(ctx, "-p", a.configPath)
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
