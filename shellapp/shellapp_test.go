package shellapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/duxlabs-dev/dux-go/ipc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	return New(Config{ID: "danktest", QSAppID: "dev.duxlabs.danktest", Version: "0.0.1"})
}

func writeShellDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shell.qml"), []byte("import QtQuick\n"), 0o644))
	return dir
}

func TestEnvPrefixDefaultsToUpperID(t *testing.T) {
	a := New(Config{ID: "danktest"})
	assert.Equal(t, "DANKTEST", a.cfg.EnvPrefix)

	a = New(Config{ID: "danktest", EnvPrefix: "CUSTOM"})
	assert.Equal(t, "CUSTOM", a.cfg.EnvPrefix)
}

func TestSessionRestartExitCode(t *testing.T) {
	assert.Equal(t, 1, New(Config{}).sessionRestartExitCode())
	assert.Equal(t, 75, New(Config{SessionRestartExitCode: 75}).sessionRestartExitCode())
}

func TestExitMatchesSignal(t *testing.T) {
	assert.True(t, exitMatchesSignal(syscall.SIGTERM, 143))
	assert.True(t, exitMatchesSignal(syscall.SIGINT, 130))
	assert.False(t, exitMatchesSignal(syscall.SIGTERM, 139))
	assert.False(t, exitMatchesSignal(syscall.SIGTERM, 1))
}

func TestRestartUsesManagedRestartHook(t *testing.T) {
	called := false
	a := New(Config{
		TryManagedRestart: func() (bool, error) {
			called = true
			return true, nil
		},
	})

	require.NoError(t, a.restart())

	assert.True(t, called)
}

func TestRestartReturnsManagedRestartError(t *testing.T) {
	a := New(Config{
		TryManagedRestart: func() (bool, error) {
			return true, errors.New("manager unavailable")
		},
	})

	assert.ErrorContains(t, a.restart(), "manager unavailable")
}

func TestPIDsReapsDeadEntries(t *testing.T) {
	a := testApp(t)

	require.NoError(t, a.writePIDFile(os.Getpid()))
	deadFile := filepath.Join(a.runtimeDir(), fmt.Sprintf("danktest-%d.pid", 999999))
	require.NoError(t, os.WriteFile(deadFile, []byte("999998"), 0o644))
	junkFile := filepath.Join(a.runtimeDir(), "danktest-junk.pid")
	require.NoError(t, os.WriteFile(junkFile, []byte("not-a-pid"), 0o644))

	pids := a.PIDs()
	assert.Contains(t, pids, os.Getpid())
	assert.NotContains(t, pids, 999998)

	_, err := os.Stat(deadFile)
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(junkFile)
	assert.True(t, os.IsNotExist(err))
}

func TestPIDsIncludesLiveParentFromFilename(t *testing.T) {
	a := testApp(t)

	parent := os.Getppid()
	file := filepath.Join(a.runtimeDir(), fmt.Sprintf("danktest-%d.pid", parent))
	require.NoError(t, os.WriteFile(file, []byte(strconv.Itoa(os.Getpid())), 0o644))

	pids := a.PIDs()
	assert.Contains(t, pids, os.Getpid())
	assert.Contains(t, pids, parent)
}

func TestLegacyIDInstanceIsFound(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	a := New(Config{ID: "danktest", LegacyIDs: []string{"oldtest"}})

	parent := os.Getppid()
	base := filepath.Join(a.runtimeDir(), fmt.Sprintf("oldtest-%d", parent))
	require.NoError(t, os.WriteFile(base+".pid", []byte(strconv.Itoa(os.Getpid())), 0o644))
	require.NoError(t, os.WriteFile(base+".session", []byte("wayland-test"), 0o644))
	require.NoError(t, os.WriteFile(base+".sock", nil, 0o644))

	assert.Contains(t, a.PIDs(), os.Getpid())
	pid, ok := a.SessionPID()
	assert.True(t, ok)
	assert.Equal(t, os.Getpid(), pid)
	socket, ok := a.SessionSocketPath()
	assert.True(t, ok)
	assert.Equal(t, base+".sock", socket)
}

func TestResolveConfigCustomPathWins(t *testing.T) {
	a := testApp(t)
	dir := writeShellDir(t)
	t.Setenv("DANKTEST_SHELL_DIR", writeShellDir(t))

	*a.CustomConfigVar() = dir
	require.NoError(t, a.ResolveConfig(nil, nil))
	assert.Equal(t, dir, a.ConfigPath())
}

func TestResolveConfigCustomPathValidated(t *testing.T) {
	a := testApp(t)

	*a.CustomConfigVar() = t.TempDir()
	err := a.ResolveConfig(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "custom config path")
}

func TestResolveConfigEnvVar(t *testing.T) {
	a := testApp(t)
	dir := writeShellDir(t)
	t.Setenv("DANKTEST_SHELL_DIR", dir)

	require.NoError(t, a.ResolveConfig(nil, nil))
	assert.Equal(t, dir, a.ConfigPath())
}

func TestResolveConfigStateFileUsedOnlyWithLiveInstance(t *testing.T) {
	a := testApp(t)
	dir := writeShellDir(t)
	require.NoError(t, os.WriteFile(a.stateFile(), []byte(dir+"\n"), 0o644))

	err := a.ResolveConfig(nil, nil)
	require.Error(t, err, "no live instance and no embedded UI")
	_, statErr := os.Stat(a.stateFile())
	assert.True(t, os.IsNotExist(statErr), "stale state file should be removed")

	require.NoError(t, os.WriteFile(a.stateFile(), []byte(dir+"\n"), 0o644))
	require.NoError(t, a.writePIDFile(os.Getpid()))
	require.NoError(t, a.ResolveConfig(nil, nil))
	assert.Equal(t, dir, a.ConfigPath())
}

type fakeEmbedded struct {
	dir string
}

func (f fakeEmbedded) Available() bool { return f.dir != "" }

func (f fakeEmbedded) Extract(string) (string, error) { return f.dir, nil }

func (f fakeEmbedded) Prune(string, string) {}

func TestResolveConfigFallsBackToEmbedded(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dir := writeShellDir(t)
	a := New(Config{ID: "danktest", Embedded: fakeEmbedded{dir: dir}})

	require.NoError(t, a.ResolveConfig(nil, nil))
	assert.Equal(t, dir, a.ConfigPath())
}

func TestResolveConfigErrorsWithoutEmbedded(t *testing.T) {
	a := testApp(t)

	err := a.ResolveConfig(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no embedded UI")
}

func TestBuildUICommandEnv(t *testing.T) {
	a := testApp(t)
	a.configPath = "/some/shell"
	a.startHidden = true
	t.Setenv("QT_QPA_PLATFORM", "")
	os.Unsetenv("QT_QPA_PLATFORM")
	t.Setenv("DANKTEST_LOG_LEVEL", "debug")

	cmd := a.buildUICommand(context.Background(), "/run/danktest-1.sock")

	assert.Equal(t, []string{"qs", "-p", "/some/shell"}, cmd.Args)
	env := strings.Join(cmd.Env, "\n")
	assert.Contains(t, env, "DANKTEST_SOCKET=/run/danktest-1.sock")
	assert.Contains(t, env, "QS_APP_ID=dev.duxlabs.danktest")
	assert.Contains(t, env, "DANKTEST_START_HIDDEN=1")
	assert.Contains(t, env, "QT_QPA_PLATFORM=wayland;xcb")
	assert.Contains(t, env, "DANKTEST_LOG_LEVEL=debug")
}

func TestUICommandWaitOutlivesForkedChildren(t *testing.T) {
	a := testApp(t)
	cmd := a.buildUICommand(context.Background(), "/run/danktest-1.sock")

	sh, err := exec.LookPath("sh")
	require.NoError(t, err)
	cmd.Path, cmd.Args, cmd.Err = sh, []string{"sh", "-c", "sleep 30 & exit 0"}, nil
	cmd.Stderr = &bytes.Buffer{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })

	start := time.Now()
	_ = cmd.Wait()
	assert.Less(t, time.Since(start), 10*time.Second)
}

func TestCallUI(t *testing.T) {
	a := testApp(t)

	srv := ipc.NewServer(ipc.Config{AppName: "danktest", APIVersion: 1},
		func(ctx context.Context, w *ipc.ConnWriter, req ipc.Request, sub *ipc.Subscriber) {
			switch req.Method {
			case "ui.show":
				w.Respond(req.ID, map[string]any{"ok": true})
			default:
				w.RespondError(req.ID, "unknown method: "+req.Method)
			}
		})
	require.NoError(t, srv.Listen())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(func() { _ = srv.Close() })

	assert.NoError(t, a.CallUI("ui.show", nil))
	assert.ErrorContains(t, a.CallUI("ui.bogus", nil), "unknown method")
}

func TestCallUIFailsWithoutInstance(t *testing.T) {
	a := testApp(t)
	assert.Error(t, a.CallUI("ui.show", nil))
}

func TestSessionFileLifecycle(t *testing.T) {
	a := testApp(t)
	t.Setenv("WAYLAND_DISPLAY", "wayland-7")

	require.NoError(t, a.writePIDFile(os.Getpid()))
	data, err := os.ReadFile(a.sessionFilePath())
	require.NoError(t, err)
	assert.Equal(t, "wayland-7", string(data))

	a.removePIDFile()
	_, err = os.Stat(a.sessionFilePath())
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(a.pidFilePath())
	assert.True(t, os.IsNotExist(err))
}

func TestSessionPIDPrefersMatchingDisplay(t *testing.T) {
	a := testApp(t)
	t.Setenv("WAYLAND_DISPLAY", "wayland-7")
	require.NoError(t, a.writePIDFile(os.Getpid()))

	otherPID := filepath.Join(a.runtimeDir(), fmt.Sprintf("danktest-%d.pid", 999999))
	require.NoError(t, os.WriteFile(otherPID, []byte(strconv.Itoa(os.Getppid())), 0o644))
	otherSession := filepath.Join(a.runtimeDir(), fmt.Sprintf("danktest-%d.session", 999999))
	require.NoError(t, os.WriteFile(otherSession, []byte("wayland-0"), 0o644))

	pid, ok := a.SessionPID()
	require.True(t, ok)
	assert.Equal(t, os.Getpid(), pid)
}

func TestSessionUIPIDMatchesOnlyCurrentDisplay(t *testing.T) {
	a := testApp(t)
	t.Setenv("WAYLAND_DISPLAY", "wayland-7")
	require.NoError(t, a.writePIDFile(os.Getpid()))

	pid, ok := a.sessionUIPID()
	require.True(t, ok)
	assert.Equal(t, os.Getpid(), pid)

	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	_, ok = a.sessionUIPID()
	assert.False(t, ok)
}

func TestSessionUIPIDIgnoresDeadInstance(t *testing.T) {
	a := testApp(t)
	t.Setenv("WAYLAND_DISPLAY", "wayland-7")

	session := filepath.Join(a.runtimeDir(), fmt.Sprintf("danktest-%d.session", 999999))
	require.NoError(t, os.WriteFile(session, []byte("wayland-7"), 0o644))
	pidFile := filepath.Join(a.runtimeDir(), fmt.Sprintf("danktest-%d.pid", 999999))
	require.NoError(t, os.WriteFile(pidFile, []byte("999999"), 0o644))

	_, ok := a.sessionUIPID()
	assert.False(t, ok)
}

func TestSessionPIDFallsBackToFirstLiveInstance(t *testing.T) {
	a := testApp(t)
	t.Setenv("WAYLAND_DISPLAY", "wayland-7")

	file := filepath.Join(a.runtimeDir(), fmt.Sprintf("danktest-%d.pid", os.Getppid()))
	require.NoError(t, os.WriteFile(file, []byte(strconv.Itoa(os.Getpid())), 0o644))

	pid, ok := a.SessionPID()
	require.True(t, ok)
	assert.Equal(t, os.Getpid(), pid)
}

func TestSessionSocketPath(t *testing.T) {
	a := testApp(t)
	t.Setenv("WAYLAND_DISPLAY", "wayland-7")
	require.NoError(t, a.writePIDFile(os.Getpid()))

	_, ok := a.SessionSocketPath()
	assert.False(t, ok)

	socket := filepath.Join(a.runtimeDir(), fmt.Sprintf("danktest-%d.sock", os.Getpid()))
	require.NoError(t, os.WriteFile(socket, nil, 0o600))

	path, ok := a.SessionSocketPath()
	require.True(t, ok)
	assert.Equal(t, socket, path)
}

func TestHotReloadDisabledOutsideHome(t *testing.T) {
	a := testApp(t)
	a.configPath = filepath.Join(t.TempDir(), "shell")

	cmd := a.buildUICommand(context.Background(), "/run/danktest-1.sock")
	assert.Contains(t, strings.Join(cmd.Env, "\n"), "DANKTEST_DISABLE_HOT_RELOAD=1")

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	a.configPath = filepath.Join(home, "shell")
	cmd = a.buildUICommand(context.Background(), "/run/danktest-1.sock")
	assert.NotContains(t, strings.Join(cmd.Env, "\n"), "DANKTEST_DISABLE_HOT_RELOAD")

	t.Setenv("DANKTEST_DISABLE_HOT_RELOAD", "0")
	a.configPath = filepath.Join(t.TempDir(), "shell")
	cmd = a.buildUICommand(context.Background(), "/run/danktest-1.sock")
	assert.NotContains(t, strings.Join(cmd.Env, "\n"), "DANKTEST_DISABLE_HOT_RELOAD=1")
}

func TestExtraEnvAppended(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	a := New(Config{ID: "danktest", ExtraEnv: func(configPath string) []string {
		return []string{"DANKTEST_EXECUTABLE=/usr/bin/danktest", "CONFIG=" + configPath}
	}})
	a.configPath = "/some/shell"

	env := strings.Join(a.buildUICommand(context.Background(), "/run/danktest-1.sock").Env, "\n")
	assert.Contains(t, env, "DANKTEST_EXECUTABLE=/usr/bin/danktest")
	assert.Contains(t, env, "CONFIG=/some/shell")
}

type fakeRecord int

const (
	recordMatching fakeRecord = iota
	recordMissing
	recordStale // UI PID reused by an unrelated process
)

func requireProcStartTimes(t *testing.T) {
	t.Helper()
	if _, ok := procStartTime(os.Getpid()); !ok {
		t.Skip("process start times need /proc")
	}
}

// fakeInstance starts a supervisor stand-in that, like superviseShell, takes its UI child down on SIGTERM.
func fakeInstance(t *testing.T, a *App, display string, managed bool, record fakeRecord) (parent, ui proc) {
	t.Helper()
	requireProcStartTimes(t)
	cmd := exec.Command("sh", "-c", `trap 'kill $c; exit 0' TERM; sleep 30 & c=$!; echo $c; wait`)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	go cmd.Wait()

	line := make([]byte, 32)
	n, err := out.Read(line)
	require.NoError(t, err)
	ui.pid, err = strconv.Atoi(strings.TrimSpace(string(line[:n])))
	require.NoError(t, err)
	parent.pid = cmd.Process.Pid
	t.Cleanup(func() {
		syscall.Kill(ui.pid, syscall.SIGKILL)
		syscall.Kill(parent.pid, syscall.SIGKILL)
	})

	var ok bool
	parent.start, ok = procStartTime(parent.pid)
	require.True(t, ok)
	ui.start, ok = procStartTime(ui.pid)
	require.True(t, ok)

	base := filepath.Join(a.runtimeDir(), fmt.Sprintf("danktest-%d", parent.pid))
	require.NoError(t, os.WriteFile(base+pidFileExtension, []byte(strconv.Itoa(ui.pid)), 0o644))
	require.NoError(t, os.WriteFile(base+sessionFileExtension, []byte(display), 0o644))
	if managed {
		require.NoError(t, os.WriteFile(base+managedFileExtension, nil, 0o644))
	}
	switch record {
	case recordMatching:
		require.NoError(t, os.WriteFile(base+startFileExtension, []byte(fmt.Sprintf("%d %d", parent.start, ui.start)), 0o644))
	case recordStale:
		require.NoError(t, os.WriteFile(base+startFileExtension, []byte(fmt.Sprintf("%d %d", parent.start+1, ui.start+1)), 0o644))
	}
	return parent, ui
}

func TestClaimSession(t *testing.T) {
	tests := []struct {
		name          string
		holderManaged bool
		record        fakeRecord
		session       bool
		wantStart     bool
		wantKilled    bool
	}{
		{"service start replaces hand-started instance", false, recordMatching, true, true, true},
		{"hand start refuses hand-started instance", false, recordMatching, false, false, false},
		{"service start refuses service instance", true, recordMatching, true, false, false},
		{"hand start refuses service instance", true, recordMatching, false, false, false},
		{"service start refuses unverifiable instance", false, recordMissing, true, false, false},
		{"hand start refuses unverifiable instance", false, recordMissing, false, false, false},
		{"stale record never signals the reused PID", false, recordStale, true, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := testApp(t)
			t.Setenv("WAYLAND_DISPLAY", "wayland-test")
			parent, ui := fakeInstance(t, a, "wayland-test", tt.holderManaged, tt.record)

			assert.Equal(t, tt.wantStart, a.claimSession(tt.session))
			assert.Equal(t, !tt.wantKilled, ui.alive(), "holder UI alive")
			assert.Equal(t, !tt.wantKilled, parent.alive(), "holder supervisor alive")
			_, ok := a.currentSessionInstance()
			assert.Equal(t, !tt.wantStart, ok, "holder still registered")
		})
	}
}

func TestClaimSessionIgnoresOtherDisplays(t *testing.T) {
	a := testApp(t)
	t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	_, ui := fakeInstance(t, a, "wayland-other", false, recordMatching)

	assert.True(t, a.claimSession(true))
	assert.True(t, ui.alive())
}

func TestClaimSessionSkipsLeftoverSessionFile(t *testing.T) {
	a := testApp(t)
	t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	// "danktest-1" sorts before any real PID, so the lookup reaches it first.
	leftover := filepath.Join(a.runtimeDir(), "danktest-1"+sessionFileExtension)
	require.NoError(t, os.WriteFile(leftover, []byte("wayland-test"), 0o644))
	_, ui := fakeInstance(t, a, "wayland-test", true, recordMatching)

	assert.False(t, a.claimSession(false))
	assert.True(t, ui.alive())
}

func TestPIDsReapsDeadInstanceFiles(t *testing.T) {
	a := testApp(t)
	dead := exec.Command("true")
	require.NoError(t, dead.Run())

	base := filepath.Join(a.runtimeDir(), "danktest-1")
	for _, ext := range instanceFileExtensions {
		require.NoError(t, os.WriteFile(base+ext, []byte(strconv.Itoa(dead.Process.Pid)), 0o644))
	}

	assert.Empty(t, a.PIDs())
	for _, ext := range instanceFileExtensions {
		assert.NoFileExists(t, base+ext)
	}
}

func TestInstanceFilesFollowPIDFile(t *testing.T) {
	a := testApp(t)
	a.sessionManaged = true

	require.NoError(t, a.writePIDFile(os.Getpid()))
	assert.FileExists(t, a.managedFilePath())
	if _, ok := procStartTime(os.Getpid()); ok {
		assert.FileExists(t, a.startFilePath())
	} else {
		assert.NoFileExists(t, a.startFilePath())
	}

	a.removePIDFile()
	assert.NoFileExists(t, a.managedFilePath())
	assert.NoFileExists(t, a.startFilePath())
}
