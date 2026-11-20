package helper

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/duxlabs-dev/dux-go/files"
)

const (
	helperProcessEnv = "DUX_GO_TEST_HELPER_PROCESS"
	userNSProbeEnv   = "DUX_GO_TEST_USERNS_PROBE"
)

func TestMain(m *testing.M) {
	switch {
	case os.Getenv(userNSProbeEnv) != "":
		if unix.Prctl(unix.PR_CAPBSET_DROP, unix.CAP_SYS_ADMIN, 0, 0, 0) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	case os.Getenv(helperProcessEnv) == "":
		os.Exit(m.Run())
	}
	UnderPkexec("test", func(args []string, stdin io.Reader, stdout io.Writer) (any, error) {
		if err := checkConfined(); err != nil {
			return nil, err
		}
		_, err := Create(args[0], 0o644, true, stdin, stdout)
		return nil, err
	})
	os.Exit(99)
}

// checkConfined reads the operation's own credentials, so a Main that skipped confinement fails the test.
func checkConfined() error {
	status, err := threadStatus(strconv.Itoa(unix.Gettid()))
	if err != nil {
		return err
	}
	if status["CapEff"] != fmt.Sprintf("%016x", uint64(keptCaps)) || status["NoNewPrivs"] != "1" || status["Umask"] != "0000" {
		return fmt.Errorf("unconfined: CapEff %s NoNewPrivs %s Umask %s", status["CapEff"], status["NoNewPrivs"], status["Umask"])
	}
	return nil
}

// helperProcess starts this test binary as the helper, as root in a new user namespace when asRoot is set.
func helperProcess(t *testing.T, asRoot bool, stdin []byte, env []string, args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(self, args...)
	cmd.Env = append(append(os.Environ(), helperProcessEnv+"=1", "PKEXEC_UID="+strconv.Itoa(os.Getuid())), env...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if asRoot {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags:  syscall.CLONE_NEWUSER,
			UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
			GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		}
	}
	return cmd, &stdout, &stderr
}

// Ubuntu's AppArmor userns restriction creates the namespace but denies its capabilities, so probe a real drop.
func startOrSkip(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	probe := exec.Command(cmd.Path)
	probe.Env = append(os.Environ(), userNSProbeEnv+"=1")
	probe.SysProcAttr = cmd.SysProcAttr
	if err := probe.Run(); err != nil {
		t.Skip("no root in a user namespace: " + err.Error())
	}
	require.NoError(t, cmd.Start())
}

func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func frames(chunks ...string) []byte {
	var buf bytes.Buffer
	for _, chunk := range chunks {
		buf.Write(binary.BigEndian.AppendUint32(nil, uint32(len(chunk))))
		buf.WriteString(chunk)
	}
	return buf.Bytes()
}

func committed(chunks ...string) []byte {
	return append(frames(chunks...), 0, 0, 0, 0)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func create(path string, stdin []byte) (string, error) {
	var out bytes.Buffer
	_, err := Create(path, 0o640, false, bytes.NewReader(stdin), &out)
	return out.String(), err
}

func TestOpenParentRefusesBadPaths(t *testing.T) {
	root := t.TempDir()
	for name, path := range map[string]string{
		"relative": "relative/a",
		"unclean":  root + "//a",
		"dotdot":   root + "/a/..",
		"root":     "/",
		"nul":      root + "/a\x00b",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := OpenParent(path)
			assert.Equal(t, files.CodeInvalid, CodeOf(err))
		})
	}
}

func TestSymlinksAreRefused(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	require.NoError(t, os.Mkdir(real, 0o755))
	target := filepath.Join(real, "f")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o644))
	require.NoError(t, os.Symlink(real, filepath.Join(root, "dirlink")))
	require.NoError(t, os.Symlink(target, filepath.Join(root, "leaf")))

	_, err := Read(filepath.Join(root, "leaf"), io.Discard)
	require.ErrorIs(t, err, ErrSymlink)
	_, err = create(filepath.Join(root, "dirlink", "new"), committed("data"))
	require.ErrorIs(t, err, ErrSymlink)
	_, err = create(filepath.Join(root, "leaf"), committed("data"))
	require.Error(t, err)

	assert.Equal(t, "x", readFile(t, target))
	assert.NoFileExists(t, filepath.Join(real, "new"))
}

func TestReadStreamsRegularFilesOnly(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f")
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("z"), 70000), 0o600))

	var out bytes.Buffer
	st, err := Read(path, &out)
	require.NoError(t, err)
	assert.Equal(t, 70000, out.Len())
	assert.Equal(t, int64(70000), st.Size)

	fifo := filepath.Join(root, "fifo")
	require.NoError(t, unix.Mkfifo(fifo, 0o600))
	_, err = Read(fifo, io.Discard)
	assert.Equal(t, files.CodeInvalid, CodeOf(err))

	_, err = Read(root, io.Discard)
	assert.Error(t, err)

	_, err = Read(filepath.Join(root, "missing"), io.Discard)
	assert.Equal(t, files.CodeNotFound, CodeOf(err))
}

func TestCreateCommitsFramesAndDropsAbortedFiles(t *testing.T) {
	old := unix.Umask(0)
	t.Cleanup(func() { unix.Umask(old) })
	root := t.TempDir()
	path := filepath.Join(root, "out")

	out, err := create(path, committed("hello ", "world"))
	require.NoError(t, err)
	assert.Equal(t, Ready+"\n", out)
	assert.Equal(t, "hello world", readFile(t, path))
	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())

	out, err = create(path, committed("again"))
	assert.Equal(t, files.CodeExists, CodeOf(err))
	assert.Empty(t, out, "nothing is announced when the create fails")
	assert.Equal(t, "hello world", readFile(t, path), "an existing file is never replaced")

	aborted := filepath.Join(root, "aborted")
	_, err = create(aborted, frames("partial"))
	require.Error(t, err)
	assert.NoFileExists(t, aborted, "an uncommitted write leaves nothing behind")

	_, err = Create(filepath.Join(root, "setuid"), 0o4755, false, bytes.NewReader(committed("x")), io.Discard)
	assert.Equal(t, files.CodeInvalid, CodeOf(err), "special bits are refused")
	assert.NoFileExists(t, filepath.Join(root, "setuid"))

	empty := filepath.Join(root, "empty")
	st, err := Create(empty, 0o644, true, bytes.NewReader(committed()), io.Discard)
	require.NoError(t, err)
	assert.Empty(t, readFile(t, empty))
	assert.Equal(t, int64(0), st.Size)
}

func TestFinishReportsTheOutcome(t *testing.T) {
	var stderr bytes.Buffer
	assert.Equal(t, 0, Finish(&stderr, map[string]string{"identity": "1:2:3:4"}, nil))
	assert.JSONEq(t, `{"identity":"1:2:3:4"}`, stderr.String())

	stderr.Reset()
	assert.Equal(t, 0, Finish(&stderr, nil, nil))
	assert.Empty(t, stderr.String())

	stderr.Reset()
	assert.Equal(t, ExitFailed, Finish(&stderr, nil, &os.PathError{Op: "open", Path: "/x", Err: unix.ENOENT}))
	assert.JSONEq(t, `{"code":"ENOENT","error":"open /x: no such file or directory"}`, stderr.String())

	stderr.Reset()
	assert.Equal(t, ExitFailed, Finish(&stderr, nil, Invalid("bad")))
	assert.JSONEq(t, `{"code":"EINVAL","error":"bad"}`, stderr.String(), "the code is not repeated in the message")
}

func TestMainRefusesOutsidePkexec(t *testing.T) {
	run := func([]string, io.Reader, io.Writer) (any, error) {
		t.Fatal("ran without pkexec")
		return nil, nil
	}
	t.Setenv("PKEXEC_UID", "")
	var stderr bytes.Buffer
	assert.Equal(t, ExitRefused, Main("test", nil, nil, nil, &stderr, run))
	assert.Contains(t, stderr.String(), "pkexec")

	if os.Getuid() == 0 {
		t.Skip("running as root")
	}
	t.Setenv("PKEXEC_UID", "1000")
	stderr.Reset()
	assert.Equal(t, ExitRefused, Main("test", nil, nil, nil, &stderr, run))
	assert.Contains(t, stderr.String(), "root")
}

func TestUnderPkexecRunsOnlyTheHelper(t *testing.T) {
	for name, args := range map[string][]string{"another command": {"run", "-c", "/somewhere"}, "no command": nil} {
		t.Run(name, func(t *testing.T) {
			cmd, _, stderr := helperProcess(t, false, nil, nil, args...)
			assert.Equal(t, ExitRefused, exitCode(cmd.Run()), stderr.String())
			assert.Contains(t, stderr.String(), "only its helper")
		})
	}

	t.Setenv("PKEXEC_UID", "")
	UnderPkexec("test", func([]string, io.Reader, io.Writer) (any, error) {
		t.Fatal("ran without pkexec")
		return nil, nil
	})
}

func TestMainConfinedRootWritesWhereTheCallerCannot(t *testing.T) {
	locked := filepath.Join(t.TempDir(), "locked")
	require.NoError(t, os.Mkdir(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	made := filepath.Join(locked, "made")

	cmd, stdout, stderr := helperProcess(t, true, committed("root"), nil, "helper", made)
	startOrSkip(t, cmd)
	require.NoError(t, cmd.Wait(), stderr.String())
	assert.Equal(t, Ready+"\n", stdout.String())

	require.NoError(t, os.Chmod(locked, 0o700))
	assert.Equal(t, "root", readFile(t, made))
}

func TestMainRefusesAForgedBoundedMarker(t *testing.T) {
	made := filepath.Join(t.TempDir(), "made")
	cmd, stdout, stderr := helperProcess(t, true, committed("root"), []string{boundedEnv + "=1"}, "helper", made)
	startOrSkip(t, cmd)
	assert.Equal(t, ExitRefused, exitCode(cmd.Wait()), stderr.String())
	assert.Contains(t, stderr.String(), "privilege drop did not take effect")
	assert.Empty(t, stdout.String())
	assert.NoFileExists(t, made)
}

func TestMainConfinesDespiteAnEmptyBoundedMarker(t *testing.T) {
	made := filepath.Join(t.TempDir(), "made")
	cmd, _, stderr := helperProcess(t, true, committed("root"), []string{boundedEnv + "="}, "helper", made)
	startOrSkip(t, cmd)
	require.NoError(t, cmd.Wait(), stderr.String())
	assert.Equal(t, "root", readFile(t, made))
}
