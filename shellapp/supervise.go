package shellapp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/duxlabs-dev/dux-go/log"
	"github.com/duxlabs-dev/dux-go/qtbase"
)

const stderrTailLimit = 8192

type stderrTail struct {
	mu     sync.Mutex
	buf    strings.Builder
	parent io.Writer
}

func (t *stderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.buf.Len() < stderrTailLimit {
		t.buf.Write(p)
	}
	if t.parent == nil {
		return len(p), nil
	}
	return t.parent.Write(p)
}

func (t *stderrTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

func getProcessExitCode(state *os.ProcessState) int {
	if state == nil {
		return 1
	}
	if code := state.ExitCode(); code != -1 {
		return code
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok {
		if status.Signaled() {
			return 128 + int(status.Signal())
		}
	}
	return 1
}

func (a *App) sessionRestartExitCode() int {
	if a.cfg.SessionRestartExitCode > 0 && a.cfg.SessionRestartExitCode < 256 {
		return a.cfg.SessionRestartExitCode
	}
	return 1
}

func exitMatchesSignal(sig os.Signal, exitCode int) bool {
	s, ok := sig.(syscall.Signal)
	return ok && exitCode == 128+int(s)
}

func (a *App) printBanner() {
	fmt.Fprintf(os.Stderr, "%s %s\n", binaryName(), a.cfg.Version)
}

func (a *App) RunInteractive(session bool) error {
	a.sessionManaged = session
	a.printBanner()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	backend, err := a.cfg.Boot(ctx)
	if err != nil {
		return fmt.Errorf("starting backend: %w", err)
	}
	defer backend.Close()

	if err := a.writeConfigState(); err != nil {
		log.Warnf("failed to write config state: %v", err)
	}
	defer a.clearConfigState()

	log.Infof("%s backend ready (ipc=%s)", binaryName(), backend.SocketPath())
	log.Infof("starting UI (config=%s)", a.configPath)

	if a.cfg.PreLaunch != nil {
		a.cfg.PreLaunch()
	}

	cmd := a.buildUICommand(ctx, backend.SocketPath())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	tail := &stderrTail{parent: os.Stderr}
	cmd.Stderr = tail

	return a.superviseShell(cmd, false, backend, tail)
}

func (a *App) RunDaemon(session bool) error {
	a.sessionManaged = session
	a.printBanner()

	if !slices.Contains(os.Args, "--daemon-child") {
		return a.spawnDaemonChild()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	backend, err := a.cfg.Boot(ctx)
	if err != nil {
		return fmt.Errorf("starting backend: %w", err)
	}
	defer backend.Close()

	if err := a.writeConfigState(); err != nil {
		log.Warnf("failed to write config state: %v", err)
	}
	defer a.clearConfigState()

	log.Infof("%s backend ready (ipc=%s)", binaryName(), backend.SocketPath())

	if a.cfg.PreLaunch != nil {
		a.cfg.PreLaunch()
	}

	cmd := a.buildUICommand(ctx, backend.SocketPath())

	devNull, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open /dev/null: %w", err)
	}
	defer devNull.Close()

	cmd.Stdin = devNull
	cmd.Stdout = devNull
	tail := &stderrTail{parent: devNull}
	cmd.Stderr = tail

	return a.superviseShell(cmd, true, backend, tail)
}

func (a *App) spawnDaemonChild() error {
	if testing.Testing() {
		return qtbase.ErrTestReexec
	}
	args := []string{"run", "-d", "--daemon-child"}
	if a.startHidden {
		args = append(args, "--hidden")
	}
	if a.customConfig != "" {
		args = append([]string{"-c", a.customConfig}, args...)
	}

	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting daemon: %w", err)
	}

	log.Infof("%s daemon started (pid=%d)", binaryName(), cmd.Process.Pid)
	return nil
}

func (a *App) superviseShell(cmd *exec.Cmd, silent bool, backend Backend, tail *stderrTail) error {
	startTime := time.Now()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting UI: %w", err)
	}

	if err := a.writePIDFile(cmd.Process.Pid); err != nil {
		log.Warnf("failed to write pid file: %v", err)
	}
	defer a.removePIDFile()

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1)

	var backendDone <-chan error
	if d, ok := backend.(interface{ Done() <-chan error }); ok {
		backendDone = d.Done()
	}

	exitUI := func() {
		exitCode := getProcessExitCode(cmd.ProcessState)
		if a.cfg.OnUIExit != nil {
			a.cfg.OnUIExit(exitCode, time.Since(startTime), tail.String())
		}
		os.Exit(exitCode)
	}

	exitChan := make(chan error, 1)
	go func() {
		if err := cmd.Wait(); err != nil {
			exitChan <- fmt.Errorf("UI exited: %w", err)
			return
		}
		exitChan <- fmt.Errorf("UI exited")
	}()

	for {
		select {
		case sig := <-sigChan:
			if sig == syscall.SIGUSR1 {
				if a.sessionManaged {
					log.Infof("received SIGUSR1, exiting for systemd restart")
					cmd.Process.Signal(syscall.SIGTERM)
					os.Exit(a.sessionRestartExitCode())
				}
				log.Infof("received SIGUSR1, spawning detached restart")
				execDetachedRestart(os.Getpid())
				return nil
			}

			select {
			case <-exitChan:
				if exitMatchesSignal(sig, getProcessExitCode(cmd.ProcessState)) {
					return nil
				}
				exitUI()
			case <-time.After(500 * time.Millisecond):
			}

			if !silent {
				log.Infof("\nreceived %v, shutting down", sig)
			}
			cmd.Process.Signal(syscall.SIGTERM)
			return nil

		case err := <-backendDone:
			if err != nil {
				log.Errorf("backend exited: %v", err)
			}
			cmd.Process.Signal(syscall.SIGTERM)
			os.Exit(1)

		case err := <-exitChan:
			if !silent && err != nil {
				log.Error(err)
			}
			if cmd.Process != nil {
				cmd.Process.Signal(syscall.SIGTERM)
			}
			exitUI()
		}
	}
}

func execDetachedRestart(targetPID int) {
	if testing.Testing() {
		return
	}
	selfPath, err := os.Executable()
	if err != nil {
		return
	}

	cmd := exec.Command(selfPath, "restart-detached", strconv.Itoa(targetPID))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Start()
}

func (a *App) RestartDetached(targetPIDStr string) {
	targetPID, err := strconv.Atoi(targetPIDStr)
	if err != nil {
		return
	}

	time.Sleep(200 * time.Millisecond)

	if proc, err := os.FindProcess(targetPID); err == nil {
		proc.Signal(syscall.SIGTERM)
	}

	time.Sleep(500 * time.Millisecond)

	a.Kill()
	_ = a.RunDaemon(false)
}

func (a *App) restart() error {
	if a.cfg.TryManagedRestart != nil {
		handled, err := a.cfg.TryManagedRestart()
		if err != nil {
			return fmt.Errorf("requesting managed restart: %w", err)
		}
		if handled {
			log.Infof("requested managed restart for %s", binaryName())
			return nil
		}
	}

	pids := a.otherPIDs()
	if len(pids) == 0 {
		log.Infof("no running %s instances; starting daemon", binaryName())
		return a.RunDaemon(false)
	}

	for pid := range pids {
		proc, err := os.FindProcess(pid)
		if err != nil {
			log.Errorf("finding process %d: %v", pid, err)
			continue
		}
		if proc.Signal(syscall.Signal(0)) != nil {
			continue
		}
		if err := proc.Signal(syscall.SIGUSR1); err != nil {
			log.Errorf("sending SIGUSR1 to %d: %v", pid, err)
			continue
		}
		log.Infof("sent SIGUSR1 to %s pid=%d", binaryName(), pid)
	}
	return nil
}

func (a *App) Restart() {
	if err := a.restart(); err != nil {
		log.Errorf("restarting %s: %v", binaryName(), err)
	}
}

func (a *App) Kill() {
	pids := a.otherPIDs()
	if len(pids) == 0 {
		log.Infof("no running %s instances", binaryName())
		return
	}

	for pid := range pids {
		proc, err := os.FindProcess(pid)
		if err != nil {
			log.Errorf("finding process %d: %v", pid, err)
			continue
		}
		if proc.Signal(syscall.Signal(0)) != nil {
			continue
		}
		if err := proc.Kill(); err != nil {
			log.Errorf("killing process %d: %v", pid, err)
			continue
		}
		log.Infof("killed %s pid=%d", binaryName(), pid)
	}

	a.removeAllPIDFiles()
}

func (a *App) otherPIDs() map[int]bool {
	currentPid := os.Getpid()
	unique := make(map[int]bool)
	for _, pid := range a.PIDs() {
		if pid != currentPid {
			unique[pid] = true
		}
	}
	return unique
}
