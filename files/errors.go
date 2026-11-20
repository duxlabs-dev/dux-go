package files

import (
	"errors"
	"io/fs"
	"syscall"
)

type Code string

const (
	CodeNotFound     Code = "ENOENT"
	CodeAccess       Code = "EACCES"
	CodeNotDir       Code = "ENOTDIR"
	CodeInvalid      Code = "EINVAL"
	CodeExists       Code = "EEXIST"
	CodeCrossDevice  Code = "EXDEV"
	CodeBusy         Code = "EBUSY"
	CodeIO           Code = "EIO"
	CodeNotSupported Code = "NOTSUPPORTED"
	CodeSandboxed    Code = "SANDBOXED"
	CodeConflict     Code = "CONFLICT"
)

// ErrConflict means the file no longer is the one an operation was based on.
var ErrConflict = errors.New("the file changed since it was read")

type Error struct {
	Code Code
	Path string
	Err  error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return string(e.Code) + ": " + e.Path
	}
	return string(e.Code) + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// Is matches a code that crossed a process boundary, where Err is only its message, against the usual sentinels.
func (e *Error) Is(target error) bool {
	switch target {
	case fs.ErrNotExist:
		return e.Code == CodeNotFound
	case fs.ErrPermission:
		return e.Code == CodeAccess
	case fs.ErrExist:
		return e.Code == CodeExists
	case fs.ErrInvalid:
		return e.Code == CodeInvalid
	case ErrConflict:
		return e.Code == CodeConflict
	}
	return false
}

func CodeOf(err error) Code {
	var coded *Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return CodeIO
}

func Wrap(path string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: CodeFromOS(err), Path: path, Err: err}
}

func CodeFromOS(err error) Code {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return CodeNotFound
	case errors.Is(err, fs.ErrPermission):
		return CodeAccess
	case errors.Is(err, fs.ErrExist), errors.Is(err, syscall.ENOTEMPTY):
		return CodeExists
	case errors.Is(err, syscall.ENOTDIR):
		return CodeNotDir
	case errors.Is(err, syscall.EXDEV):
		return CodeCrossDevice
	case errors.Is(err, syscall.EBUSY):
		return CodeBusy
	case errors.Is(err, fs.ErrInvalid):
		return CodeInvalid
	default:
		return CodeIO
	}
}
