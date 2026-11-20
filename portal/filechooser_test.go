package portal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalPathsDecodeFileURIsOnly(t *testing.T) {
	paths := localPaths([]string{
		"file:///home/me/My%20Notes/a%23b.md",
		"file://localhost/etc/hosts",
		"https://example.org/remote.txt",
		"file://server/share/remote.txt",
		"file://",
	})
	assert.Equal(t, []string{"/home/me/My Notes/a#b.md", "/etc/hosts"}, paths)
}

func TestCallFailureNamesAMissingPortal(t *testing.T) {
	ctx := context.Background()
	assert.ErrorIs(t, callFailure(ctx, dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"}), ErrNoPortal)
	assert.ErrorIs(t, callFailure(ctx, &dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownMethod"}), ErrNoPortal)

	other := callFailure(ctx, errors.New("broken pipe"))
	require.Error(t, other)
	assert.NotErrorIs(t, other, ErrNoPortal)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	assert.ErrorIs(t, callFailure(canceled, dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"}), context.Canceled)
}

func TestWaitMatchesTheRequestAndMapsResponseCodes(t *testing.T) {
	const path = dbus.ObjectPath("/org/freedesktop/portal/desktop/request/1_42/tok")
	response := func(at dbus.ObjectPath, code uint32, results map[string]dbus.Variant) *dbus.Signal {
		return &dbus.Signal{Path: at, Name: requestInterface + ".Response", Body: []any{code, results}}
	}
	wait := func(signals ...*dbus.Signal) (map[string]dbus.Variant, error) {
		w := &responseWaiter{path: path, signals: make(chan *dbus.Signal, len(signals))}
		for _, sig := range signals {
			w.signals <- sig
		}
		return w.wait(context.Background())
	}

	uris := map[string]dbus.Variant{"uris": dbus.MakeVariant([]string{"file:///a"})}
	results, err := wait(response("/other", 1, nil), response(path, 0, uris))
	require.NoError(t, err, "a response for another request is skipped")
	assert.Equal(t, uris, results)

	_, err = wait(response(path, 1, nil))
	assert.ErrorIs(t, err, ErrCancelled)

	_, err = wait(response(path, 2, nil))
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrCancelled)
}

func TestWaitEndsWhenTheBusCloses(t *testing.T) {
	w := &responseWaiter{signals: make(chan *dbus.Signal)}
	close(w.signals)
	done := make(chan error, 1)
	go func() {
		_, err := w.wait(context.Background())
		done <- err
	}()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, dbus.ErrClosed)
	case <-time.After(time.Second):
		t.Fatal("wait spins on a closed signal channel")
	}
}
