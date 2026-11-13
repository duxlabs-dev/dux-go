package paths_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duxlabs-dev/dux-go/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setXDGHome(t *testing.T, root string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
}

func TestDirsCreatedUnderXDGHome(t *testing.T) {
	root := t.TempDir()
	setXDGHome(t, root)
	app := paths.New("dankapp")

	tests := []struct {
		name string
		fn   func() (string, error)
		base string
	}{
		{"config", app.ConfigDir, "config"},
		{"data", app.DataDir, "data"},
		{"cache", app.CacheDir, "cache"},
		{"state", app.StateDir, "state"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := tc.fn()
			require.NoError(t, err)
			assert.Equal(t, filepath.Join(root, tc.base, "dankapp"), dir)

			info, err := os.Stat(dir)
			require.NoError(t, err)
			assert.True(t, info.IsDir())
		})
	}
}

func TestDataDirIsPrivate(t *testing.T) {
	setXDGHome(t, t.TempDir())

	dir, err := paths.New("dankapp").DataDir()
	require.NoError(t, err)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestSocketDir(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("FLATPAK_ID", "")
	os.Unsetenv("FLATPAK_ID")
	app := paths.New("dankapp")
	assert.Equal(t, runtime, app.SocketDir())

	t.Setenv("FLATPAK_ID", "dev.duxlabs.dankapp")
	assert.Equal(t, filepath.Join(runtime, "app", "dev.duxlabs.dankapp"), app.SocketDir())
	t.Setenv("FLATPAK_ID", "")
	os.Unsetenv("FLATPAK_ID")

	t.Setenv("XDG_RUNTIME_DIR", "")
	os.Unsetenv("XDG_RUNTIME_DIR")
	if os.Getuid() != 0 {
		assert.Equal(t, os.TempDir(), app.SocketDir())
	}
}

func TestSocketPathIsPerProcess(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)

	path := paths.New("dankapp").SocketPath()
	assert.True(t, strings.HasPrefix(path, runtime))
	assert.Contains(t, path, fmt.Sprintf("dankapp-%d.sock", os.Getpid()))
}

func TestXDGFallbacksToHome(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	for _, env := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(env, "")
		os.Unsetenv(env)
	}

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"config", paths.XDGConfigHome(), "/home/tester/.config"},
		{"cache", paths.XDGCacheHome(), "/home/tester/.cache"},
		{"data", paths.XDGDataHome(), "/home/tester/.local/share"},
		{"state", paths.XDGStateHome(), "/home/tester/.local/state"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, tc.got, tc.name)
	}
}

func TestExpandPath(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("FOO", "bar")

	tests := []struct {
		in   string
		want string
	}{
		{"~/config", "/home/tester/config"},
		{"$FOO/baz", "bar/baz"},
		{"/abs/path", "/abs/path"},
		{"~/a/../b", "/home/tester/b"},
	}
	for _, tc := range tests {
		got, err := paths.ExpandPath(tc.in)
		require.NoError(t, err)
		assert.Equal(t, filepath.Clean(tc.want), got)
	}
}

func TestXDGSearchDirs(t *testing.T) {
	t.Setenv("XDG_DATA_DIRS", "/opt/share: :/usr/share::")
	t.Setenv("XDG_CONFIG_DIRS", "")
	assert.Equal(t, []string{"/opt/share", "/usr/share"}, paths.XDGDataDirs())
	assert.Equal(t, []string{"/etc/xdg"}, paths.XDGConfigDirs())

	t.Setenv("XDG_DATA_DIRS", "")
	assert.Equal(t, []string{"/usr/local/share", "/usr/share"}, paths.XDGDataDirs())

	env := map[string]string{"SET": "x"}
	assert.Equal(t, "x", paths.EnvOr(func(k string) string { return env[k] }, "SET", "y"))
	assert.Equal(t, "y", paths.EnvOr(func(k string) string { return env[k] }, "UNSET", "y"))
}
