package shellapp

import (
	"os"

	"github.com/duxlabs-dev/dux-go/log"
	"github.com/spf13/cobra"
)

// Commands returns the standard shell lifecycle commands: run, restart,
// kill, and the hidden restart-detached helper.
func (a *App) Commands() []*cobra.Command {
	return []*cobra.Command{
		a.runCommand(),
		a.restartCommand(),
		a.killCommand(),
		a.restartDetachedCommand(),
	}
}

func (a *App) runCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "run",
		Short:   "Launch " + binaryName() + " (backend + UI)",
		Long:    "Start the " + binaryName() + " backend and launch the UI.",
		PreRunE: a.ResolveConfig,
		RunE: func(cmd *cobra.Command, _ []string) error {
			daemon, _ := cmd.Flags().GetBool("daemon")
			session, _ := cmd.Flags().GetBool("session")
			a.startHidden, _ = cmd.Flags().GetBool("hidden")
			isDaemonChild, _ := cmd.Flags().GetBool("daemon-child")

			if !isDaemonChild && !session && a.cfg.ShowMethod != "" && len(a.PIDs()) > 0 {
				if err := a.CallUI(a.cfg.ShowMethod, nil); err == nil {
					log.Infof("%s already running; showing window", binaryName())
					return nil
				}
			}

			if !isDaemonChild && !a.claimSession(session) {
				return nil
			}

			if err := a.applyLogFlags(cmd); err != nil {
				return err
			}
			if daemon {
				return a.RunDaemon(session)
			}
			return a.RunInteractive(session)
		},
	}

	cmd.Flags().BoolP("daemon", "d", false, "Run in daemon (background) mode")
	cmd.Flags().Bool("daemon-child", false, "Internal flag for daemon child process")
	cmd.Flags().Bool("session", false, "Session managed (e.g. systemd unit)")
	cmd.Flags().Bool("hidden", false, "Start with the window hidden (tray only)")
	cmd.Flags().String("log-level", "", "Log level: debug, info, warn, error, fatal (overrides "+a.cfg.EnvPrefix+"_LOG_LEVEL)")
	cmd.Flags().String("log-file", "", "Append logs to this file in addition to stderr (overrides "+a.cfg.EnvPrefix+"_LOG_FILE)")
	_ = cmd.Flags().MarkHidden("daemon-child")
	return cmd
}

func (a *App) applyLogFlags(cmd *cobra.Command) error {
	if v, _ := cmd.Flags().GetString("log-level"); v != "" {
		if err := os.Setenv(a.cfg.EnvPrefix+"_LOG_LEVEL", v); err != nil {
			return err
		}
	}
	if v, _ := cmd.Flags().GetString("log-file"); v != "" {
		if err := os.Setenv(a.cfg.EnvPrefix+"_LOG_FILE", v); err != nil {
			return err
		}
	}
	log.ApplyEnvOverrides()
	return nil
}

func (a *App) restartCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "restart",
		Short:   "Restart the running " + binaryName() + " instance",
		PreRunE: a.ResolveConfig,
		RunE: func(_ *cobra.Command, _ []string) error {
			return a.restart()
		},
	}
}

func (a *App) restartDetachedCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "restart-detached <pid>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		Run: func(_ *cobra.Command, args []string) {
			a.RestartDetached(args[0])
		},
	}
}

func (a *App) killCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "kill",
		Short: "Kill running " + binaryName() + " processes",
		Run: func(_ *cobra.Command, _ []string) {
			a.Kill()
		},
	}
}
