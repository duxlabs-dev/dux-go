package helper

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/duxlabs-dev/dux-go/files"
)

var ErrSymlink = &files.Error{Code: files.CodeInvalid, Err: errors.New("path goes through a symlink")}

func ValidPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && !strings.ContainsRune(path, 0)
}

// OpenParent opens path's directory with no symlink on the way; O_PATH works where it can be searched, not read.
func OpenParent(path string) (int, string, error) {
	if !ValidPath(path) {
		return -1, "", Invalid("not an absolute, clean path: " + path)
	}
	fd, err := OpenNoSymlinks(unix.AT_FDCWD, filepath.Dir(path), unix.O_PATH|unix.O_DIRECTORY, 0)
	if err != nil {
		return -1, "", err
	}
	return fd, filepath.Base(path), nil
}

func OpenNoSymlinks(dirfd int, path string, flags int, mode uint32) (int, error) {
	how := unix.OpenHow{Flags: uint64(flags | unix.O_CLOEXEC), Mode: uint64(mode), Resolve: unix.RESOLVE_NO_SYMLINKS}
	fd, err := unix.Openat2(dirfd, path, &how)
	switch {
	case errors.Is(err, unix.ELOOP):
		return -1, ErrSymlink
	case err != nil:
		return -1, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return fd, nil
}

func ProcPath(fd int) string { return "/proc/self/fd/" + strconv.Itoa(fd) }

// OpenRegular returns an O_PATH handle, so a FIFO or device is refused before anything opens it for I/O.
func OpenRegular(parent int, name, path string) (int, unix.Stat_t, error) {
	var st unix.Stat_t
	handle, err := OpenNoSymlinks(parent, name, unix.O_PATH, 0)
	if err != nil {
		return -1, st, err
	}
	if err := unix.Fstat(handle, &st); err != nil {
		unix.Close(handle)
		return -1, st, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		unix.Close(handle)
		return -1, st, Invalid("not a regular file: " + path)
	}
	return handle, st, nil
}

func Read(path string, out io.Writer) (unix.Stat_t, error) {
	parent, name, err := OpenParent(path)
	if err != nil {
		return unix.Stat_t{}, err
	}
	defer unix.Close(parent)

	handle, st, err := OpenRegular(parent, name, path)
	if err != nil {
		return st, err
	}
	defer unix.Close(handle)

	fd, err := unix.Open(ProcPath(handle), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		return st, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	_, err = io.Copy(out, file)
	return st, err
}

// Create removes the new file unless the stream commits; sync also fsyncs the file and its directory.
func Create(path string, perm uint32, sync bool, in io.Reader, ready io.Writer) (unix.Stat_t, error) {
	var st unix.Stat_t
	// Under umask 0 as root, a setuid or setgid bit would make a root-owned setuid file.
	if perm&^0o777 != 0 {
		return st, Invalid("a new file takes only permission bits")
	}
	parent, name, err := OpenParent(path)
	if err != nil {
		return st, err
	}
	defer unix.Close(parent)

	fd, err := OpenNoSymlinks(parent, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, perm)
	if err != nil {
		return st, err
	}
	file := os.NewFile(uintptr(fd), path)
	err = Announce(ready)
	if err == nil {
		err = CopyFrames(file, in)
	}
	if err == nil && sync {
		err = file.Sync()
	}
	if err == nil {
		err = unix.Fstat(fd, &st)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = unix.Unlinkat(parent, name, 0)
		return st, err
	}
	if !sync {
		return st, nil
	}
	return st, SyncDir(parent, path)
}

func Announce(ready io.Writer) error {
	_, err := io.WriteString(ready, Ready+"\n")
	return err
}

// Frames are a big-endian uint32 length and that many bytes; a zero length commits.
func CopyFrames(dst io.Writer, src io.Reader) error {
	var header [4]byte
	for {
		if _, err := io.ReadFull(src, header[:]); err != nil {
			return errors.New("write aborted before it was committed")
		}
		length := binary.BigEndian.Uint32(header[:])
		if length == 0 {
			return nil
		}
		if _, err := io.CopyN(dst, src, int64(length)); err != nil {
			return err
		}
	}
}

func SyncDir(parent int, path string) error {
	fd, err := unix.Open(ProcPath(parent), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err == nil {
		err = unix.Fsync(fd)
		unix.Close(fd)
	}
	if err != nil {
		return &os.PathError{Op: "fsync", Path: filepath.Dir(path), Err: err}
	}
	return nil
}
