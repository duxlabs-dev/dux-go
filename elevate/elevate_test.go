package elevate

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/duxlabs-dev/dux-go/elevate/helper"
	"github.com/duxlabs-dev/dux-go/files"
)

const (
	fakeEnv     = "DUX_GO_ELEVATE_FAKE_HELPER"
	fakeExitEnv = "DUX_GO_ELEVATE_FAKE_EXIT"
	// The fake helper sleeps this long before it starts, like a password prompt, or after it is done.
	fakePromptEnv = "DUX_GO_ELEVATE_FAKE_PROMPT"
	fakeStuckEnv  = "DUX_GO_ELEVATE_FAKE_STUCK"
)

type size struct {
	Size int64 `json:"size"`
}

// The test binary stands in for pkexec: "<binary> <exe> helper <op> <path>" runs the operation unprivileged.
func TestMain(m *testing.M) {
	if code := os.Getenv(fakeExitEnv); code != "" {
		n, _ := strconv.Atoi(code)
		os.Exit(n)
	}
	if os.Getenv(fakeEnv) == "" {
		os.Exit(m.Run())
	}
	args := os.Args[1:]
	if len(args) != 4 || args[1] != "helper" {
		fmt.Fprintln(os.Stderr, "bad fake invocation")
		os.Exit(127)
	}
	if pause, err := time.ParseDuration(os.Getenv(fakePromptEnv)); err == nil {
		time.Sleep(pause)
	}
	var result any
	var err error
	switch op, path := args[2], args[3]; op {
	case "read":
		st, readErr := helper.Read(path, os.Stdout)
		result, err = size{st.Size}, readErr
	case "write":
		st, createErr := helper.Create(path, 0o644, false, os.Stdin, os.Stdout)
		result, err = size{st.Size}, createErr
	default:
		err = helper.Invalid("unknown operation: " + op)
	}
	if pause, err := time.ParseDuration(os.Getenv(fakeStuckEnv)); err == nil {
		time.Sleep(pause)
	}
	os.Exit(helper.Finish(os.Stderr, result, err))
}

func fakeClient(t *testing.T, env ...string) *Client {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	return &Client{Pkexec: self, Exe: "app", Env: append([]string{fakeEnv + "=1"}, env...)}
}

func TestPolicyCoversOnlyThisBinary(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "bin", "app")
	require.NoError(t, os.MkdirAll(filepath.Dir(exe), 0o755))
	require.NoError(t, os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755))
	link := filepath.Join(dir, "app-link")
	require.NoError(t, os.Symlink(exe, link))

	actions := filepath.Join(dir, "actions")
	require.NoError(t, os.Mkdir(actions, 0o755))
	policy := `<policyconfig><action id="x"><annotate key="org.freedesktop.policykit.exec.path">` + link + `</annotate></action></policyconfig>`
	require.NoError(t, os.WriteFile(filepath.Join(actions, "app.policy"), []byte(policy), 0o644))

	assert.True(t, policyCovers("app.policy", exe, []string{"/nonexistent", actions}), "the annotation is resolved through symlinks")
	assert.False(t, policyCovers("app.policy", filepath.Join(dir, "other"), []string{actions}))
	assert.False(t, policyCovers("other.policy", exe, []string{actions}))
	assert.Equal(t, "", annotatedPath("<policyconfig/>"))
}

func TestAvailableExplainsWhatIsMissing(t *testing.T) {
	t.Setenv("XDG_DATA_DIRS", t.TempDir())
	t.Setenv("FLATPAK_ID", "com.example.Sandbox")
	client := &Client{Policy: "app.policy", Pkexec: "/bin/true", Exe: "/bin/true"}
	assert.Equal(t, ReasonSandboxed, client.Available())

	t.Setenv("FLATPAK_ID", "")
	if _, err := os.Stat("/.flatpak-info"); err == nil {
		t.Skip("running inside flatpak")
	}
	assert.Equal(t, ReasonNoPolicy, client.Available())

	data := t.TempDir()
	t.Setenv("XDG_DATA_DIRS", data)
	exe := filepath.Join(data, "app")
	require.NoError(t, os.WriteFile(exe, nil, 0o755))
	actions := filepath.Join(data, "polkit-1", "actions")
	require.NoError(t, os.MkdirAll(actions, 0o755))
	policy := `<annotate key="org.freedesktop.policykit.exec.path">` + exe + `</annotate>`
	require.NoError(t, os.WriteFile(filepath.Join(actions, "app.policy"), []byte(policy), 0o644))
	assert.Equal(t, "", (&Client{Policy: "app.policy", Pkexec: "/bin/true", Exe: exe}).Available())

	missing := &Client{Policy: "app.policy", Pkexec: "/nonexistent/pkexec", Exe: "/bin/true"}
	assert.Equal(t, ReasonNoPkexec, missing.Available())
	_, err := missing.Call("read", "/x")
	var unavailable *Unavailable
	require.ErrorAs(t, err, &unavailable)
	assert.Equal(t, ReasonNoPkexec, unavailable.Reason)
}

func TestFailureMapsExitCodesAndHelperReports(t *testing.T) {
	self, err := os.Executable()
	require.NoError(t, err)
	exitWith := func(code int) error {
		cmd := exec.Command(self)
		cmd.Env = append(os.Environ(), fakeExitEnv+"="+strconv.Itoa(code))
		return cmd.Run()
	}

	err = failure(exitWith(126), nil)
	assert.ErrorIs(t, err, fs.ErrPermission)
	assert.Contains(t, err.Error(), "not granted")

	err = failure(exitWith(127), []byte("Error executing command as another user: Not authorized\n"))
	assert.ErrorIs(t, err, fs.ErrPermission)

	err = failure(exitWith(127), []byte("Error executing command as another user: No authentication agent\n"))
	assert.Equal(t, files.CodeNotSupported, files.CodeOf(err))
	assert.Contains(t, err.Error(), "No authentication agent")

	err = failure(exitWith(1), []byte("noise\n{\"code\":\"CONFLICT\",\"error\":\"the file changed\"}\n"))
	assert.ErrorIs(t, err, files.ErrConflict)
	assert.Equal(t, "CONFLICT: the file changed", err.Error())

	err = failure(exitWith(3), []byte("garbage"))
	assert.Equal(t, files.CodeIO, files.CodeOf(err))

	err = failure(errors.New("fork failed"), nil)
	assert.Equal(t, files.CodeIO, files.CodeOf(err))
}

func TestWriterAndReaderRoundTripThroughTheHelper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	client := fakeClient(t)

	big := bytes.Repeat([]byte("abc"), frameSize)
	w, err := client.Writer("write", path)
	require.NoError(t, err)
	n, err := w.Write(big)
	require.NoError(t, err)
	assert.Equal(t, len(big), n)
	require.NoError(t, w.Close())
	require.NoError(t, w.Close(), "closing twice is harmless")
	var written size
	require.NoError(t, w.Result(&written))
	assert.Equal(t, int64(len(big)), written.Size)

	r, err := client.Reader("read", path)
	require.NoError(t, err)
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	assert.Equal(t, big, got, "several frames arrive in order")
	var read size
	require.NoError(t, r.Result(&read))
	assert.Equal(t, int64(len(big)), read.Size)

	out, err := client.Call("read", path)
	require.NoError(t, err)
	assert.Equal(t, big, out)
}

func TestHelperFailuresSurfaceWhenTheyHappen(t *testing.T) {
	root := t.TempDir()
	client := fakeClient(t)

	_, err := client.Reader("read", filepath.Join(root, "missing"))
	assert.ErrorIs(t, err, fs.ErrNotExist, "a refused read is reported by Reader itself")

	existing := filepath.Join(root, "exists")
	require.NoError(t, os.WriteFile(existing, []byte("old"), 0o644))
	_, err = client.Writer("write", existing)
	assert.ErrorIs(t, err, fs.ErrExist, "a refused write is reported by Writer itself")

	empty := filepath.Join(root, "empty")
	require.NoError(t, os.WriteFile(empty, nil, 0o644))
	r, err := client.Reader("read", empty)
	require.NoError(t, err)
	body, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Empty(t, body)

	_, err = client.Call("read", filepath.Join(root, "missing"))
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestWriterSplitsFramesAndResultWaitsForExit(t *testing.T) {
	var sent bytes.Buffer
	w := &Writer{pipe: nopWriteCloser{&sent}}
	_, err := w.Write(make([]byte, frameSize+1))
	require.NoError(t, err)
	var lengths []uint32
	for sent.Len() > 0 {
		n := binary.BigEndian.Uint32(sent.Next(4))
		lengths = append(lengths, n)
		sent.Next(int(n))
	}
	assert.Equal(t, []uint32{frameSize, 1}, lengths)

	w.waited = true
	w.stderr.WriteString(`{"size":1}`)
	assert.Error(t, w.Result(&size{}), "a helper that timed out may still be writing stderr")
}

func TestAbortLeavesNothingBehind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aborted")
	w, err := fakeClient(t).Writer("write", path)
	require.NoError(t, err)
	_, err = w.Write([]byte("partial"))
	require.NoError(t, err)
	_, err = w.Write(nil)
	require.NoError(t, err)
	require.NoError(t, w.Abort())
	assert.NoFileExists(t, path, "an empty write is not the commit frame")
}

func TestStuckHelperIsNotWaitedOnForever(t *testing.T) {
	waitAfterEOF = 2 * time.Second
	t.Cleanup(func() { waitAfterEOF = 30 * time.Second })
	root := t.TempDir()

	w, err := fakeClient(t, fakePromptEnv+"=2500ms").Writer("write", filepath.Join(root, "slow"))
	require.NoError(t, err, "the time spent before the helper is ready is not bounded")
	require.NoError(t, w.Close())

	w, err = fakeClient(t, fakeStuckEnv+"=6s").Writer("write", filepath.Join(root, "stuck"))
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- w.Close() }()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.Equal(t, files.CodeIO, files.CodeOf(err))
		assert.Error(t, w.Result(&size{}), "a helper still running after the timeout is not read")
		assert.Equal(t, err, w.Close(), "a second Close repeats the failure")
	case <-time.After(4 * time.Second):
		t.Fatal("waited on a helper that never exits")
	}
}
