package atomicfile

import "golang.org/x/sys/unix"

const fallocKeepSize = unix.FALLOC_FL_KEEP_SIZE

// Tests replace it to act like a filesystem that cannot reserve.
var fallocate = unix.Fallocate
