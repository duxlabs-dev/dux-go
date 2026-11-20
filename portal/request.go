package portal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const requestInterface = "org.freedesktop.portal.Request"

// ErrCancelled indicates the user dismissed the portal interaction.
var ErrCancelled = errors.New("portal: request cancelled")

func newHandleToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "dankgo" + hex.EncodeToString(b[:])
}

// requestPath predicts the Request object path the portal creates for a call
// carrying the given handle_token, per the Request documentation.
func requestPath(conn *dbus.Conn, token string) dbus.ObjectPath {
	sender := strings.ReplaceAll(strings.TrimPrefix(conn.Names()[0], ":"), ".", "_")
	return dbus.ObjectPath(objectPath + "/request/" + sender + "/" + token)
}

// responseWaiter subscribes to a Request object's Response signal before the
// portal method is called, closing the race between call and signal.
type responseWaiter struct {
	conn    *dbus.Conn
	path    dbus.ObjectPath
	signals chan *dbus.Signal
}

func newResponseWaiter(conn *dbus.Conn, path dbus.ObjectPath) (*responseWaiter, error) {
	if err := conn.AddMatchSignal(matchResponse(path)...); err != nil {
		return nil, fmt.Errorf("subscribe portal response: %w", err)
	}

	w := &responseWaiter{conn: conn, path: path, signals: make(chan *dbus.Signal, 4)}
	conn.Signal(w.signals)
	return w, nil
}

func matchResponse(path dbus.ObjectPath) []dbus.MatchOption {
	return []dbus.MatchOption{
		dbus.WithMatchInterface(requestInterface),
		dbus.WithMatchMember("Response"),
		dbus.WithMatchObjectPath(path),
	}
}

// redirect re-subscribes on the actual handle returned by the portal when it
// differs from the predicted path (pre-0.9 portal backends).
func (w *responseWaiter) redirect(path dbus.ObjectPath) error {
	if path == w.path {
		return nil
	}
	if err := w.conn.AddMatchSignal(matchResponse(path)...); err != nil {
		return fmt.Errorf("subscribe portal response: %w", err)
	}
	_ = w.conn.RemoveMatchSignal(matchResponse(w.path)...)
	w.path = path
	return nil
}

func (w *responseWaiter) wait(ctx context.Context) (map[string]dbus.Variant, error) {
	for {
		select {
		case sig, ok := <-w.signals:
			// godbus closes the channel when the connection drops.
			if !ok {
				return nil, dbus.ErrClosed
			}
			if sig.Path != w.path || sig.Name != requestInterface+".Response" || len(sig.Body) < 2 {
				continue
			}
			code, ok := sig.Body[0].(uint32)
			if !ok {
				return nil, fmt.Errorf("malformed portal response")
			}
			results, _ := sig.Body[1].(map[string]dbus.Variant)
			switch code {
			case 0:
				return results, nil
			case 1:
				return nil, ErrCancelled
			default:
				return nil, fmt.Errorf("portal request failed (code %d)", code)
			}
		case <-ctx.Done():
			w.closeRequest()
			return nil, ctx.Err()
		}
	}
}

// closeRequest dismisses the dialog of a caller that gave up.
func (w *responseWaiter) closeRequest() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = w.conn.Object(busName, w.path).CallWithContext(ctx, requestInterface+".Close", 0).Err
}

func (w *responseWaiter) close() {
	_ = w.conn.RemoveMatchSignal(matchResponse(w.path)...)
	w.conn.RemoveSignal(w.signals)
}
