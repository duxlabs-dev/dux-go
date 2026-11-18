//go:build !linux

package atomicfile

import "golang.org/x/sys/unix"

const fallocKeepSize = 0

// posix_fallocate would grow the file, so other systems skip the reservation and rely on the backup.
var fallocate = func(int, uint32, int64, int64) error { return unix.ENOSYS }
