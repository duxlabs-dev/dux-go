// Package elevate runs an app's hidden helper command as root through pkexec; elevate/helper is the root side.
package elevate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/duxlabs-dev/dux-go/elevate/helper"
	"github.com/duxlabs-dev/dux-go/files"
	"github.com/duxlabs-dev/dux-go/paths"
	"github.com/duxlabs-dev/dux-go/portal"
)

const (
	ReasonSandboxed = "SANDBOXED"
	ReasonNoPkexec  = "NOPKEXEC"
	ReasonNoPolicy  = "NOPOLICY"
	ReasonNoBinary  = "NOBINARY"
)

const frameSize = 1 << 20

// Bounds only the wait after the last byte, since the user cannot signal a stuck root process; the prompt is earlier.
var waitAfterEOF = 30 * time.Second

type Unavailable struct {
	Reason string
}

func (u *Unavailable) Error() string { return "elevation unavailable: " + u.Reason }

// Client starts "<binary> helper <op> <args>" through pkexec.
type Client struct {
	// Policy is the polkit policy file that must name this binary, such as dev.duxlabs.edit.policy.
	Policy string
	// Pkexec and Exe default to pkexec on PATH and this binary.
	Pkexec string
	Exe    string
	Env    []string

	once   sync.Once
	exe    string
	exeErr error
}

// Available returns "" when the helper can be offered, otherwise one of the Reason codes.
func (c *Client) Available() string {
	if portal.InFlatpak() {
		return ReasonSandboxed
	}
	if _, err := c.pkexecPath(); err != nil {
		return ReasonNoPkexec
	}
	exe, err := c.executable()
	if err != nil {
		return ReasonNoBinary
	}
	if !policyCovers(c.Policy, exe, actionDirs()) {
		return ReasonNoPolicy
	}
	return ""
}

func (c *Client) pkexecPath() (string, error) {
	if c.Pkexec == "" {
		return exec.LookPath("pkexec")
	}
	if _, err := os.Stat(c.Pkexec); err != nil {
		return "", err
	}
	return c.Pkexec, nil
}

func (c *Client) executable() (string, error) {
	c.once.Do(func() {
		if c.Exe != "" {
			c.exe = c.Exe
			return
		}
		exe, err := os.Executable()
		if err == nil {
			exe, err = filepath.EvalSymlinks(exe)
		}
		c.exe, c.exeErr = exe, err
	})
	return c.exe, c.exeErr
}

func actionDirs() []string {
	dirs := []string{"/usr/share/polkit-1/actions"}
	for _, dir := range paths.XDGDataDirs() {
		dirs = append(dirs, filepath.Join(dir, "polkit-1", "actions"))
	}
	return dirs
}

func policyCovers(policy, exe string, dirs []string) bool {
	for _, dir := range dirs {
		body, err := os.ReadFile(filepath.Join(dir, policy))
		if err != nil {
			continue
		}
		if annotatedPath(string(body)) == exe {
			return true
		}
	}
	return false
}

func annotatedPath(policy string) string {
	const key = `key="org.freedesktop.policykit.exec.path">`
	_, after, ok := strings.Cut(policy, key)
	if !ok {
		return ""
	}
	value, _, ok := strings.Cut(after, "<")
	if !ok {
		return ""
	}
	value = strings.TrimSpace(value)
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		return value
	}
	return resolved
}

func (c *Client) command(op string, args ...string) (*exec.Cmd, error) {
	pkexec, err := c.pkexecPath()
	if err != nil {
		return nil, &Unavailable{Reason: ReasonNoPkexec}
	}
	exe, err := c.executable()
	if err != nil {
		return nil, &Unavailable{Reason: ReasonNoBinary}
	}
	cmd := exec.Command(pkexec, append([]string{exe, "helper", op}, args...)...)
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	return cmd, nil
}

func (c *Client) Call(op string, args ...string) ([]byte, error) {
	cmd, err := c.command(op, args...)
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, failure(err, stderr.Bytes())
	}
	return stdout.Bytes(), nil
}

// Reader reads op's stdout. A helper that fails before its first byte fails here.
func (c *Client) Reader(op string, args ...string) (*Reader, error) {
	cmd, err := c.command(op, args...)
	if err != nil {
		return nil, err
	}
	r := &Reader{process: process{cmd: cmd}}
	cmd.Stderr = &r.stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &files.Error{Code: files.CodeIO, Err: err}
	}
	if err := cmd.Start(); err != nil {
		return nil, &files.Error{Code: files.CodeIO, Err: err}
	}
	r.pipe, r.buffered = stdout, bufio.NewReader(stdout)
	if _, err := r.buffered.Peek(1); err != nil {
		if waitErr := r.wait(); waitErr != nil {
			return nil, waitErr
		}
	}
	return r, nil
}

// Writer returns once the helper is ready for the file's bytes. Nothing is written unless Close commits.
func (c *Client) Writer(op string, args ...string) (*Writer, error) {
	cmd, err := c.command(op, args...)
	if err != nil {
		return nil, err
	}
	w := &Writer{process: process{cmd: cmd}}
	cmd.Stderr = &w.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, &files.Error{Code: files.CodeIO, Err: err}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &files.Error{Code: files.CodeIO, Err: err}
	}
	if err := cmd.Start(); err != nil {
		return nil, &files.Error{Code: files.CodeIO, Err: err}
	}
	w.pipe = stdin
	line, readErr := bufio.NewReader(stdout).ReadString('\n')
	if readErr != nil || strings.TrimSpace(line) != helper.Ready {
		stdout.Close()
		if err := w.finish(); err != nil {
			return nil, err
		}
		return nil, &files.Error{Code: files.CodeIO, Err: errors.New("the helper did not confirm the file")}
	}
	return w, nil
}

// stderr is read only once the helper has exited, since until then exec still writes it.
type process struct {
	cmd    *exec.Cmd
	stderr bytes.Buffer
	waited bool
	exited bool
	err    error
}

func (p *process) wait() error {
	if p.waited {
		return p.err
	}
	p.waited = true
	p.exited, p.err = waitBounded(p.cmd, &p.stderr)
	return p.err
}

func (p *process) Result(v any) error {
	if !p.exited {
		return &files.Error{Code: files.CodeIO, Err: errors.New("the helper has not finished")}
	}
	return json.Unmarshal(lastLine(p.stderr.Bytes()), v)
}

type Reader struct {
	process
	pipe     io.ReadCloser
	buffered *bufio.Reader
}

// Wait closes the pipe, so a read error after it is EOF, not a failure.
func (r *Reader) Read(p []byte) (int, error) {
	n, err := r.buffered.Read(p)
	if err == nil {
		return n, nil
	}
	if waitErr := r.wait(); waitErr != nil {
		return n, waitErr
	}
	return n, io.EOF
}

func (r *Reader) Close() error {
	r.pipe.Close()
	return r.wait()
}

type Writer struct {
	process
	pipe io.WriteCloser
}

func (w *Writer) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := min(len(p), frameSize)
		if _, err := w.pipe.Write(binary.BigEndian.AppendUint32(nil, uint32(n))); err != nil {
			return written, w.failed(err)
		}
		if _, err := w.pipe.Write(p[:n]); err != nil {
			return written, w.failed(err)
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

func (w *Writer) Close() error {
	if w.waited {
		return w.err
	}
	if _, err := w.pipe.Write([]byte{0, 0, 0, 0}); err != nil {
		return w.failed(err)
	}
	return w.finish()
}

func (w *Writer) Abort() error {
	if w.waited {
		return nil
	}
	_ = w.finish()
	return nil
}

func (w *Writer) failed(writeErr error) error {
	if err := w.finish(); err != nil {
		return err
	}
	return writeErr
}

func (w *Writer) finish() error {
	w.pipe.Close()
	return w.wait()
}

func waitBounded(cmd *exec.Cmd, stderr *bytes.Buffer) (bool, error) {
	result := make(chan error, 1)
	go func() { result <- cmd.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), waitAfterEOF)
	defer cancel()
	select {
	case err := <-result:
		if err == nil {
			return true, nil
		}
		return true, failure(err, stderr.Bytes())
	case <-ctx.Done():
		return false, &files.Error{Code: files.CodeIO, Err: errors.New("the administrator helper did not exit")}
	}
}

func failure(err error, stderr []byte) error {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return &files.Error{Code: files.CodeIO, Err: err}
	}

	var reported helper.Failure
	if json.Unmarshal(lastLine(stderr), &reported) == nil && reported.Code != "" {
		return &files.Error{Code: files.Code(reported.Code), Err: errors.New(reported.Error)}
	}

	switch exit.ExitCode() {
	case 126:
		return &files.Error{Code: files.CodeAccess, Err: errors.New("administrator authorization was not granted")}
	case 127:
		if bytes.Contains(stderr, []byte("Not authorized")) {
			return &files.Error{Code: files.CodeAccess, Err: errors.New("administrator authorization failed")}
		}
		return &files.Error{Code: files.CodeNotSupported, Err: errors.New("pkexec failed: " + firstLine(stderr))}
	}
	return &files.Error{Code: files.CodeIO, Err: errors.New("helper exited " + strconv.Itoa(exit.ExitCode()) + ": " + firstLine(stderr))}
}

func lastLine(out []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	return lines[len(lines)-1]
}

func firstLine(out []byte) string {
	line, _, _ := bytes.Cut(bytes.TrimSpace(out), []byte("\n"))
	return string(line)
}
