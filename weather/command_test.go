package weather

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommand(t *testing.T) {
	script := filepath.Join(t.TempDir(), "provider")
	body := `#!/bin/sh
case "$1" in
hit) [ "$(cat)" = '{"latitude":46.95,"longitude":7.45}' ] && printf '{"current":{"time":"2026-11-02T14:00","temperature_2m":7},"hourly":{"time":["2026-11-02T14:00"]},"daily":{"time":["2026-11-02"]}}' ;;
broken) echo "no API key" >&2; exit 1 ;;
large) head -c 3000000 /dev/zero ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	client := New(Options{Resolve: func(provider Provider) (Source, bool) {
		return Source{Fetch: Command(script, string(provider))}, true
	}})

	if result, err := lookup(client, "hit"); err != nil || value(result.Forecast.Current.Temperature) != 7.0 {
		t.Fatalf("hit = %+v, %v", result, err)
	}
	if _, err := lookup(client, "empty"); err == nil || !strings.Contains(err.Error(), "no forecast") {
		t.Errorf("empty output error = %v", err)
	}
	if _, err := lookup(client, "broken"); err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Errorf("failing command error = %v", err)
	}
	if _, err := lookup(client, "large"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized output error = %v", err)
	}
}

// A program that runs out of time is killed with what it started: the shell's sleep,
// which holds stdout open, must be gone too, and Run must not wait for it.
func TestCommandTimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	script, pidFile := filepath.Join(dir, "provider"), filepath.Join(dir, "child")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30 &\necho $! > \"$1\"\nwait\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := Command(script, pidFile)(ctx, Request{})
	if elapsed := time.Since(started); err == nil || !strings.Contains(err.Error(), "timed out after 100ms") || elapsed > 600*time.Millisecond {
		t.Errorf("hanging command = %v after %s", err, elapsed)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the program's child %d outlived it", pid)
		}
	}
}
