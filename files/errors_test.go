package files

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrorMatchesSentinelsByCode(t *testing.T) {
	remote := func(code Code) error { return &Error{Code: code, Err: errors.New("reported by a helper")} }
	assert.ErrorIs(t, remote(CodeNotFound), fs.ErrNotExist)
	assert.ErrorIs(t, remote(CodeAccess), fs.ErrPermission)
	assert.ErrorIs(t, remote(CodeExists), fs.ErrExist)
	assert.ErrorIs(t, remote(CodeInvalid), fs.ErrInvalid)
	assert.ErrorIs(t, remote(CodeConflict), ErrConflict)
	assert.NotErrorIs(t, remote(CodeIO), fs.ErrNotExist)
	assert.NotErrorIs(t, remote(CodeNotFound), fs.ErrExist)
}
