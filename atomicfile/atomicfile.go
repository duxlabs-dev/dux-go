// Package atomicfile saves files without losing their mode, owner, labels, ACLs, symlinks or hard links.
package atomicfile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type Options struct {
	// Mode for a file that does not exist yet; 0 means 0666 minus the umask.
	Mode os.FileMode
	// BackupDir gets a copy of the original bytes during an in-place overwrite. Empty means no copy.
	BackupDir string
}

// KeptXattrs are the attributes a replaced file must keep: its SELinux label and its ACL.
func KeptXattrs() []string {
	return []string{"security.selinux", "system.posix_acl_access"}
}

// Stat returns the "dev:ino:size:mtimeNs" identity of the file path resolves to.
func Stat(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return Identity(info), nil
}

// Identity is what Stat returns, for a FileInfo already at hand.
func Identity(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return identity(0, 0, info.Size(), info.ModTime().UnixNano())
	}
	return identity(st.Dev, st.Ino, st.Size, st.Mtim.Nano())
}

// StatIdentity is what Stat returns, for a stat buffer already at hand.
func StatIdentity(st *unix.Stat_t) string {
	return identity(st.Dev, st.Ino, st.Size, st.Mtim.Nano())
}

func identity(dev, ino uint64, size, mtimeNs int64) string {
	return fmt.Sprintf("%d:%d:%d:%d", dev, ino, size, mtimeNs)
}

// Writable reports whether the effective user may write path, or create it when it does not exist.
func Writable(path string) bool {
	err := unix.Faccessat(unix.AT_FDCWD, path, unix.W_OK, unix.AT_EACCESS)
	if errors.Is(err, unix.ENOENT) {
		err = unix.Faccessat(unix.AT_FDCWD, filepath.Dir(path), unix.W_OK|unix.X_OK, unix.AT_EACCESS)
	}
	return err == nil
}

// Resolve follows symlinks, including a dangling last one, so a save writes the target and keeps the link.
func Resolve(path string) (string, error) {
	for range 40 {
		// A relative link is read from the real directory, so its ".." is not taken from the path as written.
		parent, base := split(path)
		dir, err := filepath.EvalSymlinks(parent)
		if err != nil {
			return "", err
		}
		if base == "." || base == ".." {
			return filepath.EvalSymlinks(path)
		}
		path = filepath.Join(dir, base)
		info, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return path, nil
		case err != nil:
			return "", err
		case info.Mode()&os.ModeSymlink == 0:
			return path, nil
		}
		link, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(link) {
			link = dir + string(filepath.Separator) + link
		}
		path = link
	}
	return "", unix.ELOOP
}

// split cuts the last element off without cleaning, which would resolve ".." before the links in front of it.
func split(path string) (string, string) {
	path = strings.TrimRight(path, "/")
	i := strings.LastIndexByte(path, '/')
	switch {
	case path == "":
		return "/", "."
	case i < 0:
		return ".", path
	case i == 0:
		return "/", path[1:]
	}
	return path[:i], path[i+1:]
}

// Save writes data to path and returns the new identity.
func Save(path string, data []byte, opts Options) (string, error) {
	target, err := Resolve(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		mode := opts.Mode
		if mode == 0 {
			mode = 0o666 &^ umask()
		}
		if err := replace(target, data, mode, nil); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	case info.IsDir():
		return "", unix.EISDIR
	case inPlace(target, info):
		if err := overwrite(target, info, data, opts.BackupDir); err != nil {
			return "", err
		}
	default:
		if err := replace(target, data, info.Mode(), info); err != nil {
			return "", err
		}
	}
	return Stat(target)
}

// WriteFile replaces path itself through a synced temp file and a rename. Unlike Save it keeps no owner, mode or links.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	return replace(path, data, mode, nil)
}

// WriteFileMkdir is WriteFile after creating the parent directories with dirMode.
func WriteFileMkdir(path string, data []byte, mode, dirMode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return err
	}
	return WriteFile(path, data, mode)
}

// A rename would split a hard link, hand the file to us, or is impossible in a read-only directory.
func inPlace(target string, info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return true
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if ok && (st.Nlink > 1 || int(st.Uid) != os.Geteuid()) {
		return true
	}
	dirWritable := unix.Faccessat(unix.AT_FDCWD, filepath.Dir(target), unix.W_OK|unix.X_OK, unix.AT_EACCESS) == nil
	fileWritable := unix.Faccessat(unix.AT_FDCWD, target, unix.W_OK, unix.AT_EACCESS) == nil
	return !dirWritable && fileWritable
}

func overwrite(target string, info os.FileInfo, data []byte, backupDir string) error {
	// Reading a FIFO or a device to back it up could block or never end.
	backingUp := backupDir != "" && info.Mode().IsRegular()
	flag := os.O_WRONLY
	if backingUp {
		flag = os.O_RDWR
	}
	f, err := os.OpenFile(target, flag, 0)
	if err != nil {
		return err
	}
	var backup *os.File
	if backingUp {
		if backup, err = copyAside(f, backupDir); err != nil {
			f.Close()
			return err
		}
	}
	err = writeInPlace(f, data)
	switch {
	case backup == nil:
	case err == nil:
		dropBackup(backup)
	default:
		err = restore(f, backup, err)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// WriteInPlace overwrites f with data and syncs it. Blocks are reserved first, so a full disk leaves the file intact.
func WriteInPlace(f *os.File, data []byte) error {
	return writeInPlace(f, data)
}

func writeInPlace(f *os.File, data []byte) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	regular := info.Mode().IsRegular()
	if regular {
		if err := reserve(f, int64(len(data))); err != nil {
			return err
		}
	}
	if _, err := f.WriteAt(data, 0); err != nil {
		return err
	}
	if regular {
		if err := f.Truncate(int64(len(data))); err != nil {
			return err
		}
	}
	return f.Sync()
}

// Tests replace it to act like a filesystem that cannot reserve.
var fallocate = unix.Fallocate

func reserve(f *os.File, size int64) error {
	if size == 0 {
		return nil
	}
	for {
		err := fallocate(int(f.Fd()), unix.FALLOC_FL_KEEP_SIZE, 0, size)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, unix.EINTR):
			continue
		// NFSv3, FUSE and older tmpfs cannot reserve; the write goes ahead and only the backup covers a full disk.
		case errors.Is(err, unix.EOPNOTSUPP), errors.Is(err, unix.ENOSYS):
			return nil
		}
		return &os.PathError{Op: "fallocate", Path: f.Name(), Err: err}
	}
}

func copyAside(f *os.File, dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	backup, err := os.CreateTemp(dir, ".inplace-*.orig")
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(backup, f)
	if err == nil {
		err = backup.Sync()
	}
	if err != nil {
		dropBackup(backup)
		return nil, err
	}
	return backup, nil
}

// restore puts the original bytes back; if that fails too, the copy stays and the error names it.
func restore(f, backup *os.File, cause error) error {
	err := copyBack(f, backup)
	if err != nil {
		backup.Close()
		return fmt.Errorf("%w (the original is kept in %s)", cause, backup.Name())
	}
	dropBackup(backup)
	return cause
}

func copyBack(f, backup *os.File) error {
	info, err := backup.Stat()
	if err != nil {
		return err
	}
	if _, err := io.Copy(io.NewOffsetWriter(f, 0), io.NewSectionReader(backup, 0, info.Size())); err != nil {
		return err
	}
	if err := f.Truncate(info.Size()); err != nil {
		return err
	}
	return f.Sync()
}

func dropBackup(backup *os.File) {
	backup.Close()
	os.Remove(backup.Name())
}

func replace(target string, data []byte, mode os.FileMode, original os.FileInfo) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := fill(tmp, data, mode, target, original); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, target); err != nil {
		os.Remove(name)
		return err
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return nil
}

func fill(tmp *os.File, data []byte, mode os.FileMode, target string, original os.FileInfo) error {
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if original != nil {
		if st, ok := original.Sys().(*syscall.Stat_t); ok {
			_ = tmp.Chown(int(st.Uid), int(st.Gid))
		}
	}
	if err := tmp.Chmod(mode & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)); err != nil {
		return err
	}
	// The ACL goes after chmod, which would otherwise rewrite its mask.
	if original != nil {
		copyXattrs(target, tmp)
	}
	return tmp.Sync()
}

// Best effort: a filesystem without labels or ACLs, or a label we may not set, must not fail the save.
func copyXattrs(target string, tmp *os.File) {
	for _, name := range xattrNames(target) {
		size, err := unix.Lgetxattr(target, name, nil)
		if err != nil {
			continue
		}
		buf := make([]byte, size)
		if size > 0 {
			if size, err = unix.Lgetxattr(target, name, buf); err != nil {
				continue
			}
		}
		_ = unix.Fsetxattr(int(tmp.Fd()), name, buf[:size], 0)
	}
}

func xattrNames(target string) []string {
	names := KeptXattrs()
	size, err := unix.Llistxattr(target, nil)
	if err != nil || size <= 0 {
		return names
	}
	buf := make([]byte, size)
	size, err = unix.Llistxattr(target, buf)
	if err != nil {
		return names
	}
	for name := range strings.SplitSeq(string(buf[:size]), "\x00") {
		if strings.HasPrefix(name, "user.") {
			names = append(names, name)
		}
	}
	return names
}

var fsync = (*os.File).Sync

// Some filesystems refuse fsync on a directory, and a directory we may not read cannot be opened for it.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	switch {
	case errors.Is(err, fs.ErrPermission):
		return nil
	case err != nil:
		return err
	}
	defer d.Close()
	err = fsync(d)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
		return nil
	}
	return err
}

// Reading the umask through /proc avoids setting it, which would race with other goroutines creating files.
func umask() os.FileMode {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0o022
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		value, found := strings.CutPrefix(line, "Umask:")
		if !found {
			continue
		}
		mask, err := strconv.ParseUint(strings.TrimSpace(value), 8, 32)
		if err != nil {
			return 0o022
		}
		return os.FileMode(mask)
	}
	return 0o022
}
