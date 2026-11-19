package files

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListUserDirsSkipsMissingAndKeepsOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, key := range userDirOrder {
		t.Setenv(key, "")
	}
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".config"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(home, "Downloads"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(home, "Bilder"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(home, "Code"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".config", "user-dirs.dirs"), []byte(
		"XDG_PROJECTS_DIR=\"$HOME/Code\"\nXDG_PICTURES_DIR=\"$HOME/Bilder\"\nXDG_DOWNLOAD_DIR=\"$HOME/Downloads\"\nXDG_MUSIC_DIR=\"$HOME/Gone\"\n"), 0o644))

	dirs := listUserDirs()

	assert.Equal(t, []UserDir{
		{Key: "home", Name: filepath.Base(home), Path: home, IconName: "user-home"},
		{Key: "download", Name: "Downloads", Path: filepath.Join(home, "Downloads"), IconName: "folder-download"},
		{Key: "pictures", Name: "Bilder", Path: filepath.Join(home, "Bilder"), IconName: "folder-pictures"},
		{Key: "projects", Name: "Code", Path: filepath.Join(home, "Code"), IconName: "folder-projects"},
	}, dirs)
}

func TestListUserDirsPrefersTheEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, key := range userDirOrder {
		t.Setenv(key, "")
	}
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".config"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(home, "Docs"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(home, "Documents"), 0o755))
	t.Setenv("XDG_DOCUMENTS_DIR", filepath.Join(home, "Docs"))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".config", "user-dirs.dirs"), []byte(
		"XDG_DOCUMENTS_DIR=\"$HOME/Documents\"\n"), 0o644))

	dirs := listUserDirs()

	require.Len(t, dirs, 2)
	assert.Equal(t, filepath.Join(home, "Docs"), dirs[1].Path)
}

func TestFolderIconsReloadWhenUserDirsFileChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, key := range userDirOrder {
		t.Setenv(key, "")
	}
	userDirsPath := filepath.Join(home, ".config", "user-dirs.dirs")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".config"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(home, "Code"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(home, "Workspace"), 0o755))
	require.NoError(t, os.WriteFile(userDirsPath, []byte("XDG_PROJECTS_DIR=\"$HOME/Code\"\n"), 0o644))

	b := newBuilder(nil, nil)
	entries, err := b.readDir(home)
	require.NoError(t, err)
	assert.Equal(t, "folder-projects", byName(t, entries, "Code").IconName)
	assert.Equal(t, "folder", byName(t, entries, "Workspace").IconName)

	require.NoError(t, os.WriteFile(userDirsPath, []byte("XDG_PROJECTS_DIR=\"$HOME/Workspace\"\n"), 0o644))

	entries, err = b.readDir(home)
	require.NoError(t, err)
	assert.Equal(t, "folder", byName(t, entries, "Code").IconName)
	assert.Equal(t, "folder-projects", byName(t, entries, "Workspace").IconName)
}
