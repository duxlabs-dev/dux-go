package trash

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func setupHomeTrash(t *testing.T) (homeRoot string, trashDir string) {
	t.Helper()
	homeRoot = t.TempDir()
	xdg := filepath.Join(homeRoot, ".local", "share")
	if err := os.MkdirAll(xdg, 0o700); err != nil {
		t.Fatalf("mkdir xdg: %v", err)
	}
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("HOME", homeRoot)

	// Without this, Count() and Empty() reach the machine's real per-mount
	// trash dirs, and Empty() deletes what it finds there.
	prev := mountPoints
	mountPoints = func() []string { return nil }
	t.Cleanup(func() { mountPoints = prev })

	trashDir = filepath.Join(xdg, "Trash")
	return homeRoot, trashDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestPutHomeTrashAbsolutePath(t *testing.T) {
	homeRoot, trashDir := setupHomeTrash(t)

	src := filepath.Join(homeRoot, "doc.txt")
	writeFile(t, src, "hi")

	entry, err := Put(src)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if entry.Name != "doc.txt" {
		t.Errorf("name = %q, want doc.txt", entry.Name)
	}
	if entry.OriginalPath != src {
		t.Errorf("originalPath = %q, want %q", entry.OriginalPath, src)
	}
	if entry.TrashDir != trashDir {
		t.Errorf("trashDir = %q, want %q", entry.TrashDir, trashDir)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still exists: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(trashDir, "info", "doc.txt.trashinfo"))
	if err != nil {
		t.Fatalf("read trashinfo: %v", err)
	}
	if !strings.HasPrefix(string(body), "[Trash Info]\n") {
		t.Errorf("trashinfo missing header: %q", body)
	}
	if !strings.Contains(string(body), "Path="+src+"\n") {
		t.Errorf("Path key missing or wrong: %q", body)
	}
	if !strings.Contains(string(body), "DeletionDate=") {
		t.Errorf("DeletionDate missing: %q", body)
	}
}

func TestPutPercentEncodesPath(t *testing.T) {
	homeRoot, trashDir := setupHomeTrash(t)

	name := "spaces & %.txt"
	src := filepath.Join(homeRoot, name)
	writeFile(t, src, "x")

	if _, err := Put(src); err != nil {
		t.Fatalf("Put: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(trashDir, "info", name+".trashinfo"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := "Path=" + filepath.Dir(src) + "/spaces%20&%20%25.txt"
	if !strings.Contains(string(body), want) {
		t.Errorf("expected %q in %q", want, body)
	}
}

func TestPutCollisionGetsUniqueName(t *testing.T) {
	homeRoot, trashDir := setupHomeTrash(t)

	for i := range 3 {
		src := filepath.Join(homeRoot, "dup.txt")
		writeFile(t, src, "x")
		if _, err := Put(src); err != nil {
			t.Fatalf("Put #%d: %v", i, err)
		}
	}

	want := []string{"dup.txt", "dup.2.txt", "dup.3.txt"}
	for _, n := range want {
		if _, err := os.Stat(filepath.Join(trashDir, "files", n)); err != nil {
			t.Errorf("expected %s in trash: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(trashDir, "info", n+".trashinfo")); err != nil {
			t.Errorf("expected %s.trashinfo: %v", n, err)
		}
	}
}

func TestListAndCount(t *testing.T) {
	homeRoot, _ := setupHomeTrash(t)

	if n, _ := Count(); n != 0 {
		t.Errorf("initial count = %d, want 0", n)
	}
	entries, _ := List()
	if len(entries) != 0 {
		t.Errorf("initial list len = %d, want 0", len(entries))
	}

	for _, n := range []string{"a.txt", "b.txt", "c.log"} {
		src := filepath.Join(homeRoot, n)
		writeFile(t, src, n)
		if _, err := Put(src); err != nil {
			t.Fatalf("Put %s: %v", n, err)
		}
	}

	got, _ := Count()
	if got != 3 {
		t.Errorf("count = %d, want 3", got)
	}
	entries, _ = List()
	if len(entries) != 3 {
		t.Errorf("list len = %d, want 3", len(entries))
	}
	for _, e := range entries {
		if e.OriginalPath == "" {
			t.Errorf("entry %s: empty OriginalPath", e.Name)
		}
		if _, err := time.Parse("2006-01-02T15:04:05", e.DeletionDate); err != nil {
			t.Errorf("entry %s: bad DeletionDate %q: %v", e.Name, e.DeletionDate, err)
		}
	}
}

func TestEmptyClearsAll(t *testing.T) {
	homeRoot, trashDir := setupHomeTrash(t)

	for _, n := range []string{"x", "y", "z"} {
		src := filepath.Join(homeRoot, n)
		writeFile(t, src, n)
		if _, err := Put(src); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if n, _ := Count(); n != 3 {
		t.Fatalf("pre-empty count = %d", n)
	}

	if err := Empty(); err != nil {
		t.Fatalf("Empty: %v", err)
	}

	if n, _ := Count(); n != 0 {
		t.Errorf("post-empty count = %d, want 0", n)
	}
	for _, sub := range []string{"files", "info"} {
		ents, err := os.ReadDir(filepath.Join(trashDir, sub))
		if err != nil {
			t.Fatalf("readdir %s: %v", sub, err)
		}
		if len(ents) != 0 {
			t.Errorf("%s/ has %d entries, want 0", sub, len(ents))
		}
	}
}

func TestRestoreToOriginalPath(t *testing.T) {
	homeRoot, trashDir := setupHomeTrash(t)

	src := filepath.Join(homeRoot, "sub", "dir", "thing.txt")
	writeFile(t, src, "payload")

	entry, err := Put(src)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	os.RemoveAll(filepath.Join(homeRoot, "sub"))

	if err := Restore(entry.Name, trashDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if string(body) != "payload" {
		t.Errorf("restored content = %q, want %q", body, "payload")
	}

	if _, err := os.Stat(entry.InfoPath); !os.IsNotExist(err) {
		t.Errorf("info file still present: %v", err)
	}
	if _, err := os.Stat(entry.FilesPath); !os.IsNotExist(err) {
		t.Errorf("files entry still present: %v", err)
	}
}

func TestRestoreRefusesToOverwrite(t *testing.T) {
	homeRoot, trashDir := setupHomeTrash(t)

	src := filepath.Join(homeRoot, "keep.txt")
	writeFile(t, src, "v1")

	entry, err := Put(src)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	writeFile(t, src, "v2-blocking")

	err = Restore(entry.Name, trashDir)
	if err == nil {
		t.Fatalf("expected error on conflicting restore, got nil")
	}
	if !strings.Contains(err.Error(), "exists") {
		t.Errorf("error %q does not mention conflict", err)
	}

	body, _ := os.ReadFile(src)
	if string(body) != "v2-blocking" {
		t.Errorf("blocking file altered: %q", body)
	}
}

func TestPutDirectory(t *testing.T) {
	homeRoot, trashDir := setupHomeTrash(t)

	dir := filepath.Join(homeRoot, "myfolder")
	writeFile(t, filepath.Join(dir, "child.txt"), "inside")

	entry, err := Put(dir)
	if err != nil {
		t.Fatalf("Put dir: %v", err)
	}
	if !entry.IsDir {
		t.Errorf("IsDir = false, want true")
	}

	moved := filepath.Join(trashDir, "files", "myfolder", "child.txt")
	body, err := os.ReadFile(moved)
	if err != nil {
		t.Fatalf("read moved child: %v", err)
	}
	if string(body) != "inside" {
		t.Errorf("child content = %q", body)
	}
}

func TestIsValidSharedTrashRejectsSymlink(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "real")
	if err := os.MkdirAll(target, os.ModeSticky|0o777); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}

	link := filepath.Join(tmp, ".Trash")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if isValidSharedTrash(link) {
		t.Errorf("symlinked .Trash accepted; spec requires rejection")
	}
}

func TestIsValidSharedTrashRequiresStickyBit(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".Trash")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if isValidSharedTrash(dir) {
		t.Errorf(".Trash without sticky bit accepted; spec requires rejection")
	}

	if err := os.Chmod(dir, os.ModeSticky|0o777); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if !isValidSharedTrash(dir) {
		t.Errorf(".Trash with sticky bit rejected; spec accepts it")
	}
}

func TestPathEncodeRoundTrip(t *testing.T) {
	cases := []string{
		"/home/u/file.txt",
		"/path with spaces/and-symbols & %.txt",
		"relative/path/é unicode.md",
	}
	for _, in := range cases {
		got := pathDecode(pathEncode(in))
		if got != in {
			t.Errorf("round-trip %q -> %q", in, got)
		}
	}
}

func TestEffectiveMountPoint(t *testing.T) {
	mounts := []string{
		"/",
		"/mnt/data",
		"/mnt/data/nested",
		"/mnt/data/nested/deep",
		"/run/media/user/USB Drive",
	}

	cases := []struct {
		path      string
		wantMount string
		wantFound bool
	}{
		{"/mnt/data/nested/deep/file.txt", "/mnt/data/nested/deep", true},
		{"/mnt/data/nested/deep", "/mnt/data/nested/deep", true},
		{"/mnt/data/nested/file.txt", "/mnt/data/nested", true},
		{"/mnt/data/file.txt", "/mnt/data", true},
		{"/mnt/data-other/file.txt", "/", true},
		{"/mnt/data2", "/", true},
		{"/etc/hosts", "/", true},
		{"/", "/", true},
		{"/run/media/user/USB Drive/doc.txt", "/run/media/user/USB Drive", true},
	}

	for _, tc := range cases {
		gotMount, gotFound := effectiveMountPoint(tc.path, mounts)
		if gotFound != tc.wantFound || gotMount != tc.wantMount {
			t.Errorf("effectiveMountPoint(%q) = (%q, %v), want (%q, %v)",
				tc.path, gotMount, gotFound, tc.wantMount, tc.wantFound)
		}
	}

	// Without root mount in list
	subset := []string{"/mnt/data", "/mnt/data/nested"}
	if m, ok := effectiveMountPoint("/home/user/file.txt", subset); ok {
		t.Errorf("expected not found, got %q", m)
	}
	if m, ok := effectiveMountPoint("/mnt/data/file.txt", subset); !ok || m != "/mnt/data" {
		t.Errorf("expected /mnt/data, got (%q, %v)", m, ok)
	}

	// Empty mounts
	if m, ok := effectiveMountPoint("/any/path", nil); ok {
		t.Errorf("expected not found for empty mounts, got %q", m)
	}
}

func TestPutAcrossDistinctMountsSameStDev(t *testing.T) {
	tmp := t.TempDir()
	homeMount := filepath.Join(tmp, "home")
	srcMount := filepath.Join(tmp, "mnt", "btrfs-subvol")

	homeRoot := filepath.Join(homeMount, "user")
	xdg := filepath.Join(homeRoot, ".local", "share")
	if err := os.MkdirAll(xdg, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("HOME", homeRoot)

	prev := mountPoints
	mountPoints = func() []string {
		return []string{homeMount, srcMount}
	}
	t.Cleanup(func() { mountPoints = prev })

	// Both paths reside in tmp and have the same st_dev.
	src := filepath.Join(srcMount, "sub folder", "my doc & %1.txt")
	writeFile(t, src, "content-for-trash")

	homeDev, err := fsDeviceWalkUp(filepath.Join(xdg, "Trash"))
	if err != nil {
		t.Fatalf("home fsDevice: %v", err)
	}
	srcDev, err := fsDevice(src)
	if err != nil {
		t.Fatalf("src fsDevice: %v", err)
	}
	if homeDev != srcDev {
		t.Fatalf("test prerequisite failed: st_dev differs (%d vs %d)", homeDev, srcDev)
	}

	entry, err := Put(src)
	if err != nil {
		t.Fatalf("Put across mounts: %v", err)
	}

	uid := strconv.Itoa(os.Getuid())
	expectedTrashDir := filepath.Join(srcMount, ".Trash-"+uid)
	if entry.TrashDir != expectedTrashDir {
		t.Errorf("entry.TrashDir = %q, want %q", entry.TrashDir, expectedTrashDir)
	}

	expectedRel := "sub folder/my doc & %1.txt"
	if entry.OriginalPath != expectedRel {
		t.Errorf("entry.OriginalPath = %q, want %q", entry.OriginalPath, expectedRel)
	}

	// Verify file was moved out of original location
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still exists at %s", src)
	}

	// Verify content in trash
	body, err := os.ReadFile(entry.FilesPath)
	if err != nil {
		t.Fatalf("read trashed file: %v", err)
	}
	if string(body) != "content-for-trash" {
		t.Errorf("trashed content = %q, want %q", body, "content-for-trash")
	}

	// Verify .trashinfo path encoding preserves '/' and relative path, and percent-encodes spaces and '%'
	infoBody, err := os.ReadFile(entry.InfoPath)
	if err != nil {
		t.Fatalf("read info: %v", err)
	}
	wantPathLine := "Path=sub%20folder/my%20doc%20&%20%251.txt\n"
	if !strings.Contains(string(infoBody), wantPathLine) {
		t.Errorf("expected %q in .trashinfo, got:\n%s", wantPathLine, infoBody)
	}

	// Verify Restore restores back to original location
	if err := Restore(entry.Name, entry.TrashDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restoredBody, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if string(restoredBody) != "content-for-trash" {
		t.Errorf("restored content = %q", restoredBody)
	}
}

func TestPutAcrossDistinctMountsSharedTrashSticky(t *testing.T) {
	tmp := t.TempDir()
	homeMount := filepath.Join(tmp, "home")
	srcMount := filepath.Join(tmp, "mnt", "volume")

	homeRoot := filepath.Join(homeMount, "user")
	xdg := filepath.Join(homeRoot, ".local", "share")
	if err := os.MkdirAll(xdg, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("HOME", homeRoot)

	// Create valid shared trash $topdir/.Trash with sticky bit
	sharedTrash := filepath.Join(srcMount, ".Trash")
	if err := os.MkdirAll(sharedTrash, os.ModeSticky|0o777); err != nil {
		t.Fatalf("mkdir shared trash: %v", err)
	}
	// os.MkdirAll does not set sticky bit on existing dirs, ensure chmod
	if err := os.Chmod(sharedTrash, os.ModeSticky|0o777); err != nil {
		t.Fatalf("chmod sticky: %v", err)
	}

	prev := mountPoints
	mountPoints = func() []string {
		return []string{homeMount, srcMount}
	}
	t.Cleanup(func() { mountPoints = prev })

	src := filepath.Join(srcMount, "file.txt")
	writeFile(t, src, "shared-data")

	entry, err := Put(src)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	uid := strconv.Itoa(os.Getuid())
	expectedTrashDir := filepath.Join(srcMount, ".Trash", uid)
	if entry.TrashDir != expectedTrashDir {
		t.Errorf("TrashDir = %q, want %q", entry.TrashDir, expectedTrashDir)
	}
	if entry.OriginalPath != "file.txt" {
		t.Errorf("OriginalPath = %q, want %q", entry.OriginalPath, "file.txt")
	}

	// Restore
	if err := Restore(entry.Name, entry.TrashDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if b, _ := os.ReadFile(src); string(b) != "shared-data" {
		t.Errorf("restored content mismatch: %q", b)
	}
}

func TestPutSameMountPreservesHomeTrash(t *testing.T) {
	tmp := t.TempDir()
	mountRoot := filepath.Join(tmp, "sharedmount")
	homeRoot := filepath.Join(mountRoot, "home", "user")
	xdg := filepath.Join(homeRoot, ".local", "share")
	if err := os.MkdirAll(xdg, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("HOME", homeRoot)

	prev := mountPoints
	mountPoints = func() []string {
		return []string{mountRoot}
	}
	t.Cleanup(func() { mountPoints = prev })

	src := filepath.Join(mountRoot, "otherdir", "doc.txt")
	writeFile(t, src, "same-mount-data")

	entry, err := Put(src)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	expectedTrashDir := filepath.Join(xdg, "Trash")
	if entry.TrashDir != expectedTrashDir {
		t.Errorf("TrashDir = %q, want %q", entry.TrashDir, expectedTrashDir)
	}
	if entry.OriginalPath != src {
		t.Errorf("OriginalPath = %q, want absolute path %q", entry.OriginalPath, src)
	}

	infoBody, err := os.ReadFile(entry.InfoPath)
	if err != nil {
		t.Fatalf("read info: %v", err)
	}
	if !strings.Contains(string(infoBody), "Path="+src+"\n") {
		t.Errorf("expected absolute path in info, got:\n%s", infoBody)
	}
}

func TestPutRootMountHandling(t *testing.T) {
	tmp := t.TempDir()
	rootMount := filepath.Join(tmp, "rootfs")
	homeMount := filepath.Join(tmp, "home")

	homeRoot := filepath.Join(homeMount, "user")
	xdg := filepath.Join(homeRoot, ".local", "share")
	if err := os.MkdirAll(xdg, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("HOME", homeRoot)

	prev := mountPoints
	mountPoints = func() []string {
		return []string{rootMount, homeMount}
	}
	t.Cleanup(func() { mountPoints = prev })

	src := filepath.Join(rootMount, "etc", "test.conf")
	writeFile(t, src, "conf-content")

	entry, err := Put(src)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	uid := strconv.Itoa(os.Getuid())
	expectedTrashDir := filepath.Join(rootMount, ".Trash-"+uid)
	if entry.TrashDir != expectedTrashDir {
		t.Errorf("TrashDir = %q, want %q", entry.TrashDir, expectedTrashDir)
	}
	if entry.OriginalPath != "etc/test.conf" {
		t.Errorf("OriginalPath = %q, want etc/test.conf", entry.OriginalPath)
	}

	if err := Restore(entry.Name, entry.TrashDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if b, _ := os.ReadFile(src); string(b) != "conf-content" {
		t.Errorf("restored content = %q", b)
	}
}

func TestFindTopDirWithMountPoints(t *testing.T) {
	prev := mountPoints
	mountPoints = func() []string {
		return []string{"/mnt/disk", "/mnt/disk/nested"}
	}
	t.Cleanup(func() { mountPoints = prev })

	top, err := findTopDir("/mnt/disk/nested/a/b/file.txt")
	if err != nil {
		t.Fatalf("findTopDir: %v", err)
	}
	if top != "/mnt/disk/nested" {
		t.Errorf("findTopDir = %q, want /mnt/disk/nested", top)
	}

	top, err = findTopDir("/mnt/disk/other/file.txt")
	if err != nil {
		t.Fatalf("findTopDir: %v", err)
	}
	if top != "/mnt/disk" {
		t.Errorf("findTopDir = %q, want /mnt/disk", top)
	}
}

func TestPutSymlinkedParentCrossesMount(t *testing.T) {
	tmp := t.TempDir()
	homeMount := filepath.Join(tmp, "home")
	srcMount := filepath.Join(tmp, "mnt", "external")

	homeRoot := filepath.Join(homeMount, "user")
	xdg := filepath.Join(homeRoot, ".local", "share")
	if err := os.MkdirAll(xdg, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("HOME", homeRoot)

	prev := mountPoints
	mountPoints = func() []string {
		return []string{homeMount, srcMount}
	}
	t.Cleanup(func() { mountPoints = prev })

	realDir := filepath.Join(srcMount, "real-project")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("mkdir realDir: %v", err)
	}

	symlinkDir := filepath.Join(homeRoot, "link-to-project")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	filePath := filepath.Join(symlinkDir, "document.txt")
	writeFile(t, filePath, "important-payload")

	entry, err := Put(filePath)
	if err != nil {
		t.Fatalf("Put via symlink parent: %v", err)
	}

	uid := strconv.Itoa(os.Getuid())
	expectedTrashDir := filepath.Join(srcMount, ".Trash-"+uid)
	if entry.TrashDir != expectedTrashDir {
		t.Errorf("TrashDir = %q, want %q", entry.TrashDir, expectedTrashDir)
	}

	expectedRel := "real-project/document.txt"
	if entry.OriginalPath != expectedRel {
		t.Errorf("OriginalPath = %q, want %q", entry.OriginalPath, expectedRel)
	}

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("source still exists at %s", filePath)
	}
	realFilePath := filepath.Join(realDir, "document.txt")
	if _, err := os.Stat(realFilePath); !os.IsNotExist(err) {
		t.Errorf("real source still exists at %s", realFilePath)
	}

	infoBody, err := os.ReadFile(entry.InfoPath)
	if err != nil {
		t.Fatalf("read info: %v", err)
	}
	if !strings.Contains(string(infoBody), "Path=real-project/document.txt\n") {
		t.Errorf(".trashinfo missing expected Path, got:\n%s", infoBody)
	}

	if err := Restore(entry.Name, entry.TrashDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if content, err := os.ReadFile(filePath); err != nil || string(content) != "important-payload" {
		t.Errorf("file not restored at symlink path: %q, err: %v", content, err)
	}
}

func TestPutSymlinkSourceItem(t *testing.T) {
	tmp := t.TempDir()
	homeMount := filepath.Join(tmp, "home")
	srcMount := filepath.Join(tmp, "mnt", "other")

	homeRoot := filepath.Join(homeMount, "user")
	xdg := filepath.Join(homeRoot, ".local", "share")
	if err := os.MkdirAll(xdg, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("HOME", homeRoot)

	prev := mountPoints
	mountPoints = func() []string {
		return []string{homeMount, srcMount}
	}
	t.Cleanup(func() { mountPoints = prev })

	targetFile := filepath.Join(srcMount, "target.txt")
	writeFile(t, targetFile, "target-content")

	symlinkPath := filepath.Join(homeRoot, "link-to-target")
	if err := os.Symlink(targetFile, symlinkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	entry, err := Put(symlinkPath)
	if err != nil {
		t.Fatalf("Put symlink: %v", err)
	}

	expectedTrashDir := filepath.Join(xdg, "Trash")
	if entry.TrashDir != expectedTrashDir {
		t.Errorf("TrashDir = %q, want %q", entry.TrashDir, expectedTrashDir)
	}

	linkInfo, err := os.Lstat(entry.FilesPath)
	if err != nil {
		t.Fatalf("stat trashed item: %v", err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected trashed item to be a symlink, got mode: %v", linkInfo.Mode())
	}

	targetContent, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("target was affected: %v", err)
	}
	if string(targetContent) != "target-content" {
		t.Errorf("target content modified: %q", targetContent)
	}

	if err := Restore(entry.Name, entry.TrashDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restoredInfo, err := os.Lstat(symlinkPath)
	if err != nil {
		t.Fatalf("stat restored link: %v", err)
	}
	if restoredInfo.Mode()&os.ModeSymlink == 0 {
		t.Errorf("restored item is not a symlink")
	}
	dest, err := os.Readlink(symlinkPath)
	if err != nil || dest != targetFile {
		t.Errorf("restored symlink dest = %q, want %q", dest, targetFile)
	}
}

func TestPutSymlinkParentNestedMount(t *testing.T) {
	tmp := t.TempDir()
	homeMount := filepath.Join(tmp, "home")
	parentMount := filepath.Join(tmp, "mnt", "volume")
	nestedMount := filepath.Join(parentMount, "nested")

	homeRoot := filepath.Join(homeMount, "user")
	xdg := filepath.Join(homeRoot, ".local", "share")
	if err := os.MkdirAll(xdg, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("HOME", homeRoot)

	prev := mountPoints
	mountPoints = func() []string {
		return []string{homeMount, parentMount, nestedMount}
	}
	t.Cleanup(func() { mountPoints = prev })

	nestedDir := filepath.Join(nestedMount, "dir")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	linkDir := filepath.Join(homeRoot, "link-to-nested")
	if err := os.Symlink(nestedDir, linkDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	file := filepath.Join(linkDir, "item.txt")
	writeFile(t, file, "nested-data")

	entry, err := Put(file)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	uid := strconv.Itoa(os.Getuid())
	expectedTrashDir := filepath.Join(nestedMount, ".Trash-"+uid)
	if entry.TrashDir != expectedTrashDir {
		t.Errorf("TrashDir = %q, want %q (deepest matching mount)", entry.TrashDir, expectedTrashDir)
	}
	if entry.OriginalPath != "dir/item.txt" {
		t.Errorf("OriginalPath = %q, want %q", entry.OriginalPath, "dir/item.txt")
	}

	if err := Restore(entry.Name, entry.TrashDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if content, err := os.ReadFile(file); err != nil || string(content) != "nested-data" {
		t.Errorf("file not restored: %q, err: %v", content, err)
	}
}

func TestMountInfoFallbackCombinations(t *testing.T) {
	tmp := t.TempDir()
	homeMount := filepath.Join(tmp, "home")
	srcMount := filepath.Join(tmp, "mnt", "src")
	unrelatedMount := filepath.Join(tmp, "unrelated")

	homeRoot := filepath.Join(homeMount, "user")
	xdg := filepath.Join(homeRoot, ".local", "share")
	if err := os.MkdirAll(xdg, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("HOME", homeRoot)

	srcFile := filepath.Join(srcMount, "file.txt")
	writeFile(t, srcFile, "data")

	uid := strconv.Itoa(os.Getuid())
	expectedHomeTrash := filepath.Join(xdg, "Trash")
	expectedSrcTrash := filepath.Join(srcMount, ".Trash-"+uid)

	// mockSameDevice simulates a uniform device across the entire filesystem,
	// ensuring device-boundary traversal reaches '/' deterministically.
	mockSameDevice := func(p string) (uint64, error) {
		return 100, nil
	}

	t.Run("srcFound_and_not_homeFound", func(t *testing.T) {
		prev := mountPoints
		mountPoints = func() []string {
			return []string{srcMount}
		}
		t.Cleanup(func() { mountPoints = prev })

		td, sp, err := trashDirForPath(srcFile)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if td != expectedSrcTrash {
			t.Errorf("trashDir = %q, want %q", td, expectedSrcTrash)
		}
		if sp != "file.txt" {
			t.Errorf("storedPath = %q, want file.txt", sp)
		}
	})

	t.Run("not_srcFound_and_homeFound", func(t *testing.T) {
		prev := mountPoints
		mountPoints = func() []string {
			return []string{homeMount}
		}
		t.Cleanup(func() { mountPoints = prev })

		td, _, err := trashDirForPathWithLookup(srcFile, mockSameDevice)
		// Because homeMount is known and srcFile is NOT in homeMount,
		// degraded fallback reaches '/' and must return an error rather than
		// silently selecting root-level trash or Home Trash.
		if err == nil {
			t.Fatalf("expected error when degraded fallback reaches root, got trashDir=%q", td)
		}
		if _, err := putWithLookup(srcFile, mockSameDevice); err == nil {
			t.Fatalf("Put: expected error when degraded fallback reaches root, got nil")
		}
	})

	t.Run("neither_found_with_nonempty_mounts", func(t *testing.T) {
		prev := mountPoints
		mountPoints = func() []string {
			return []string{unrelatedMount}
		}
		t.Cleanup(func() { mountPoints = prev })

		td, _, err := trashDirForPathWithLookup(srcFile, mockSameDevice)
		// Degraded mount info does not establish same mount; fallback reaching '/'
		// must return an error rather than selecting '/.Trash-<uid>' or '/.Trash/<uid>'.
		if err == nil {
			t.Fatalf("expected error when degraded fallback reaches root, got trashDir=%q", td)
		}
		if td == expectedHomeTrash || strings.HasPrefix(td, "/.Trash") {
			t.Errorf("unexpected root or home trash selected: %q", td)
		}
		if _, err := putWithLookup(srcFile, mockSameDevice); err == nil {
			t.Fatalf("Put: expected error when degraded fallback reaches root, got nil")
		}
	})

	t.Run("degraded_fallback_stops_at_non_root_boundary", func(t *testing.T) {
		prev := mountPoints
		mountPoints = func() []string {
			return []string{homeMount}
		}
		t.Cleanup(func() { mountPoints = prev })

		nonRootBoundary := filepath.Clean(srcMount)
		mockBoundaryDevice := func(p string) (uint64, error) {
			cleanP := filepath.Clean(p)
			if cleanP == nonRootBoundary || strings.HasPrefix(cleanP, nonRootBoundary+"/") {
				return 200, nil
			}
			return 100, nil
		}

		td, sp, err := trashDirForPathWithLookup(srcFile, mockBoundaryDevice)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if td != expectedSrcTrash {
			t.Errorf("trashDir = %q, want %q", td, expectedSrcTrash)
		}
		if sp != "file.txt" {
			t.Errorf("storedPath = %q, want file.txt", sp)
		}

		entry, err := putWithLookup(srcFile, mockBoundaryDevice)
		if err != nil {
			t.Fatalf("putWithLookup: %v", err)
		}
		if entry.TrashDir != expectedSrcTrash {
			t.Errorf("entry.TrashDir = %q, want %q", entry.TrashDir, expectedSrcTrash)
		}
		if err := Restore(entry.Name, entry.TrashDir); err != nil {
			t.Fatalf("Restore: %v", err)
		}
	})

	t.Run("mount_info_unavailable_len_zero", func(t *testing.T) {
		prev := mountPoints
		mountPoints = func() []string {
			return nil
		}
		t.Cleanup(func() { mountPoints = prev })

		td, _, err := trashDirForPathWithLookup(srcFile, mockSameDevice)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		// Completely unavailable mount info falls back to st_dev equality -> Home Trash.
		if td != expectedHomeTrash {
			t.Errorf("trashDir = %q, want %q on unavailable mount info fallback", td, expectedHomeTrash)
		}
	})
}

func TestWalkDeviceBoundaryDeterministic(t *testing.T) {
	// Root reached when all devices are identical
	mockUniform := func(p string) (uint64, error) { return 1, nil }
	top, err := walkDeviceBoundary("/a/b/c/d", mockUniform)
	if err != nil {
		t.Fatalf("walkDeviceBoundary: %v", err)
	}
	if top != "/" {
		t.Errorf("top = %q, want /", top)
	}

	// Boundary reached where device changes
	mockBoundary := func(p string) (uint64, error) {
		clean := filepath.Clean(p)
		if clean == "/a/b" || strings.HasPrefix(clean, "/a/b/") {
			return 2, nil
		}
		return 1, nil
	}
	top, err = walkDeviceBoundary("/a/b/c/d", mockBoundary)
	if err != nil {
		t.Fatalf("walkDeviceBoundary: %v", err)
	}
	if top != "/a/b" {
		t.Errorf("top = %q, want /a/b", top)
	}
}

func TestEffectiveEntryPathErrorOnNonExistentParent(t *testing.T) {
	nonExistent := filepath.Join(t.TempDir(), "no-such-dir", "file.txt")
	_, _, err := trashDirForPath(nonExistent)
	if err == nil {
		t.Fatalf("expected error for non-existent parent directory, got nil")
	}
}
