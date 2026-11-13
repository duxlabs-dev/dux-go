package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestSaveKeepsModeAndWritesThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.sh")
	link := filepath.Join(dir, "link.sh")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o640))
	require.NoError(t, os.Chmod(target, 0o751))
	require.NoError(t, os.Symlink("real.sh", link))

	id, err := Save(link, []byte("new"), Options{})
	require.NoError(t, err)

	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.Equal(t, os.ModeSymlink, info.Mode()&os.ModeSymlink, "the link must stay a link")
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "new", string(data))
	info, err = os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o751), info.Mode().Perm())

	current, err := Stat(link)
	require.NoError(t, err)
	assert.Equal(t, current, id)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "no temp file left behind")
}

func TestSaveCreatesThroughDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink("made.txt", link))

	_, err := Save(link, []byte("x"), Options{Mode: 0o600})
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(dir, "made.txt"))
	require.NoError(t, err)
	assert.Equal(t, "x", string(data))
}

func TestSaveKeepsHardLinks(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	require.NoError(t, os.WriteFile(a, []byte("old and longer"), 0o644))
	require.NoError(t, os.Link(a, b))

	_, err := Save(a, []byte("new"), Options{})
	require.NoError(t, err)
	data, err := os.ReadFile(b)
	require.NoError(t, err)
	assert.Equal(t, "new", string(data))
}

func TestResolveReadsRelativeLinksFromTheRealDirectory(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	sub := filepath.Join(root, "real", "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "elsewhere"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "real", "target"), []byte("REAL"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "target"), []byte("UNRELATED"), 0o644))
	require.NoError(t, os.Symlink("../target", filepath.Join(sub, "link")))
	require.NoError(t, os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(sub, "hop")))
	require.NoError(t, os.Symlink("hop/../far", filepath.Join(sub, "through")))
	require.NoError(t, os.Symlink("../made", filepath.Join(sub, "dangling")))
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(sub, alias))

	got, err := Resolve(filepath.Join(alias, "link"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "real", "target"), got)
	_, err = Save(filepath.Join(alias, "link"), []byte("NEW"), Options{})
	require.NoError(t, err)
	assert.Equal(t, "NEW", readFile(t, filepath.Join(root, "real", "target")))
	assert.Equal(t, "UNRELATED", readFile(t, filepath.Join(root, "target")), "a save must not land on an unrelated file")

	got, err = Resolve(filepath.Join(alias, "through"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "far"), got, "the .. inside a link applies after the link before it")

	got, err = Resolve(filepath.Join(alias, "dangling"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "real", "made"), got)
	_, err = Save(filepath.Join(alias, "dangling"), []byte("x"), Options{})
	require.NoError(t, err)
	assert.Equal(t, "x", readFile(t, filepath.Join(root, "real", "made")))
	assert.NoFileExists(t, filepath.Join(root, "made"))
}

func TestResolveStopsOnALinkLoop(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	require.NoError(t, os.Symlink("b", a))
	require.NoError(t, os.Symlink("a", b))

	_, err := Resolve(a)
	assert.ErrorIs(t, err, unix.ELOOP)
	_, err = Save(a, []byte("x"), Options{})
	assert.ErrorIs(t, err, unix.ELOOP)
}

// Without fallocate, RLIMIT_FSIZE makes the in-place write fail part-way, the way a full disk does on btrfs.
func TestOverwriteRestoresTheOriginalWhenTheWriteFails(t *testing.T) {
	fallocate = func(int, uint32, int64, int64) error { return unix.EOPNOTSUPP }
	t.Cleanup(func() { fallocate = unix.Fallocate })
	dir := t.TempDir()
	backups := filepath.Join(t.TempDir(), "swap")
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	original := strings.Repeat("o", 64)
	require.NoError(t, os.WriteFile(a, []byte(original), 0o644))
	require.NoError(t, os.Link(a, b))

	var limit unix.Rlimit
	require.NoError(t, unix.Getrlimit(unix.RLIMIT_FSIZE, &limit))
	lowered := limit
	lowered.Cur = 128
	require.NoError(t, unix.Setrlimit(unix.RLIMIT_FSIZE, &lowered))
	_, err := Save(a, []byte(strings.Repeat("n", 256)), Options{BackupDir: backups})
	require.NoError(t, unix.Setrlimit(unix.RLIMIT_FSIZE, &limit))

	require.ErrorIs(t, err, unix.EFBIG)
	assert.Equal(t, original, readFile(t, b), "a failed in-place write must leave the original bytes")
	entries, err := os.ReadDir(backups)
	require.NoError(t, err)
	assert.Empty(t, entries, "the copy is dropped once the original is back")

	_, err = Save(a, []byte("new"), Options{BackupDir: backups})
	require.NoError(t, err)
	assert.Equal(t, "new", readFile(t, b))
	entries, err = os.ReadDir(backups)
	require.NoError(t, err)
	assert.Empty(t, entries, "the copy is dropped after a good write")
}

func TestSaveKeepsUserXattrs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))
	if err := unix.Setxattr(path, "user.dux.test", []byte("kept"), 0); err != nil {
		t.Skip("no user xattrs here:", err)
	}
	before, err := Stat(path)
	require.NoError(t, err)

	after, err := Save(path, []byte("new"), Options{})
	require.NoError(t, err)
	require.NotEqual(t, before, after)
	buf := make([]byte, 16)
	n, err := unix.Getxattr(path, "user.dux.test", buf)
	require.NoError(t, err)
	assert.Equal(t, "kept", string(buf[:n]))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func TestWriteFileReplacesTheLinkWithTheGivenMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	link := filepath.Join(dir, "state", "link")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(target, link))

	require.NoError(t, WriteFile(link, []byte("one"), 0o600))
	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "WriteFile replaces the path, not the link target")
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Equal(t, "one", readFile(t, link))
	assert.Equal(t, "old", readFile(t, target))

	nested := filepath.Join(dir, "a", "b", "c.json")
	require.NoError(t, WriteFileMkdir(nested, []byte("two"), 0o640, 0o700))
	assert.Equal(t, "two", readFile(t, nested))
	info, err = os.Stat(filepath.Dir(nested))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	entries, err := os.ReadDir(filepath.Dir(nested))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp file left behind")
}

func TestSaveKeepsEmptyXattrs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))
	if err := unix.Setxattr(path, "user.dux.flag", nil, 0); err != nil {
		t.Skip("no user xattrs here:", err)
	}

	_, err := Save(path, []byte("new"), Options{})
	require.NoError(t, err)
	n, err := unix.Getxattr(path, "user.dux.flag", nil)
	require.NoError(t, err, "an attribute with an empty value must survive the replace")
	assert.Zero(t, n)
}

func TestReplaceReportsAFailedDirectorySync(t *testing.T) {
	t.Cleanup(func() { fsync = (*os.File).Sync })
	failWith := func(code error) {
		fsync = func(f *os.File) error {
			info, err := f.Stat()
			if err == nil && info.IsDir() {
				return &os.PathError{Op: "sync", Path: f.Name(), Err: code}
			}
			return f.Sync()
		}
	}
	path := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))

	failWith(unix.EIO)
	_, err := Save(path, []byte("new"), Options{})
	require.ErrorIs(t, err, unix.EIO, "a rename that may not be durable is not a clean save")

	failWith(unix.EINVAL)
	_, err = Save(path, []byte("newer"), Options{})
	require.NoError(t, err, "a filesystem that cannot sync a directory still saves")
	assert.Equal(t, "newer", readFile(t, path))
}
