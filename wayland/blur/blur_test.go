package blur

import (
	"encoding/binary"
	"io"
	"net"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCompositor answers one get_registry and one sync, advertising globals.
func fakeCompositor(t *testing.T, globals ...string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	ln, err := net.Listen("unix", filepath.Join(dir, "wayland-test"))
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var registry, callback uint32
		for registry == 0 || callback == 0 {
			msg := make([]byte, 12)
			if _, err := io.ReadFull(conn, msg); err != nil {
				return
			}
			newID := binary.NativeEndian.Uint32(msg[8:])
			switch binary.NativeEndian.Uint32(msg[4:]) & 0xffff {
			case 0:
				callback = newID
			case 1:
				registry = newID
			}
		}
		for i, name := range globals {
			_, _ = conn.Write(message(registry, 0, uint32(i+1), name, uint32(1)))
		}
		_, _ = conn.Write(message(callback, 0, uint32(1)))
		_, _ = io.Copy(io.Discard, conn)
	}()
}

func message(object uint32, opcode uint16, args ...any) []byte {
	var body []byte
	for _, arg := range args {
		switch v := arg.(type) {
		case uint32:
			body = binary.NativeEndian.AppendUint32(body, v)
		case string:
			body = binary.NativeEndian.AppendUint32(body, uint32(len(v)+1))
			body = append(body, v...)
			body = append(body, make([]byte, 4-len(v)%4)...)
		}
	}
	msg := binary.NativeEndian.AppendUint32(nil, object)
	msg = binary.NativeEndian.AppendUint32(msg, uint32(8+len(body))<<16|uint32(opcode))
	return append(msg, body...)
}

func TestSupportedFindsTheEffectManager(t *testing.T) {
	fakeCompositor(t, "wl_compositor", "ext_background_effect_manager_v1")
	ok, err := Supported()
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestSupportedWithoutTheEffectManager(t *testing.T) {
	fakeCompositor(t, "wl_compositor", "wl_seat")
	ok, err := Supported()
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSupportedWithoutACompositor(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("WAYLAND_DISPLAY", "missing")
	_, err := Supported()
	assert.Error(t, err)
}
