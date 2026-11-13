// Package colorscheme follows the desktop portal's light/dark preference.
package colorscheme

import (
	"sync"

	"github.com/godbus/dbus/v5"

	"github.com/duxlabs-dev/dux-go/log"
)

// Topic is the event topic apps publish State changes under.
const Topic = "colorScheme"

// Values of the portal's org.freedesktop.appearance color-scheme setting.
const (
	NoPreference uint32 = 0
	Dark         uint32 = 1
	Light        uint32 = 2
)

const (
	portalDest     = "org.freedesktop.portal.Desktop"
	portalPath     = "/org/freedesktop/portal/desktop"
	settingsIface  = "org.freedesktop.portal.Settings"
	appearanceNS   = "org.freedesktop.appearance"
	colorSchemeKey = "color-scheme"
)

// State is unavailable until the portal answers; ColorScheme is the raw portal value.
type State struct {
	Available   bool   `json:"available"`
	ColorScheme uint32 `json:"colorScheme"`
}

// Name is "dark", "light" or "none".
func (s State) Name() string {
	switch s.ColorScheme {
	case Dark:
		return "dark"
	case Light:
		return "light"
	}
	return "none"
}

type Watcher struct {
	onChange func(State)

	mu     sync.Mutex
	conn   *dbus.Conn
	closed bool
	state  State
}

// New subscribes in the background, so a slow or missing portal never holds up startup. onChange runs
// whenever the state changes, including when the portal first answers; it may be nil.
func New(onChange func(State)) *Watcher {
	w := &Watcher{onChange: onChange}
	go w.run()
	return w
}

func (w *Watcher) State() State {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

func (w *Watcher) Scheme() string { return w.State().Name() }

// Close is safe before the background subscription has finished and more than once.
func (w *Watcher) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.conn != nil {
		w.conn.Close()
		w.conn = nil
	}
}

func (w *Watcher) run() {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		log.Debugf("color scheme: no session bus: %v", err)
		return
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		conn.Close()
		return
	}
	w.conn = conn
	w.mu.Unlock()

	// Subscribing before ReadOne means a change in between is not lost.
	signals := make(chan *dbus.Signal, 4)
	conn.Signal(signals)
	// arg0 narrows the match to the appearance namespace, so other portal settings never wake us.
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(portalPath),
		dbus.WithMatchInterface(settingsIface),
		dbus.WithMatchMember("SettingChanged"),
		dbus.WithMatchArg(0, appearanceNS),
	); err != nil {
		log.Debugf("color scheme: match signal: %v", err)
		w.Close()
		return
	}

	var value dbus.Variant
	if err := conn.Object(portalDest, portalPath).Call(settingsIface+".ReadOne", 0, appearanceNS, colorSchemeKey).Store(&value); err != nil {
		log.Debugf("color scheme: portal unavailable: %v", err)
		w.Close()
		return
	}
	w.update(value)

	for sig := range signals {
		w.handle(sig)
	}
}

func (w *Watcher) handle(sig *dbus.Signal) {
	if sig.Name != settingsIface+".SettingChanged" || len(sig.Body) < 3 {
		return
	}
	namespace, _ := sig.Body[0].(string)
	key, _ := sig.Body[1].(string)
	value, ok := sig.Body[2].(dbus.Variant)
	if namespace != appearanceNS || key != colorSchemeKey || !ok {
		return
	}
	w.update(value)
}

func (w *Watcher) update(value dbus.Variant) {
	scheme, ok := asUint32(value)
	if !ok {
		return
	}
	state := State{Available: true, ColorScheme: scheme}
	w.mu.Lock()
	changed := state != w.state
	w.state = state
	w.mu.Unlock()
	if changed && w.onChange != nil {
		w.onChange(state)
	}
}

// Older portals wrap the value in a second variant.
func asUint32(value dbus.Variant) (uint32, bool) {
	switch v := value.Value().(type) {
	case uint32:
		return v, true
	case dbus.Variant:
		return asUint32(v)
	}
	return 0, false
}
