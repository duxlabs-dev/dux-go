package colorscheme

import (
	"encoding/json"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setting(namespace, key string, value any) *dbus.Signal {
	return &dbus.Signal{
		Name: settingsIface + ".SettingChanged",
		Body: []any{namespace, key, dbus.MakeVariant(value)},
	}
}

func TestReportsOnlyColorSchemeChanges(t *testing.T) {
	var changes []State
	w := &Watcher{onChange: func(s State) { changes = append(changes, s) }}
	assert.Equal(t, State{}, w.State())
	assert.Equal(t, "none", w.Scheme())

	w.handle(setting(appearanceNS, colorSchemeKey, Dark))
	w.handle(setting(appearanceNS, colorSchemeKey, Dark))
	w.handle(setting(appearanceNS, "accent-color", Light))
	w.handle(setting("org.gnome.desktop.interface", colorSchemeKey, Light))
	w.handle(setting(appearanceNS, colorSchemeKey, "not a number"))
	assert.Equal(t, "dark", w.Scheme())

	w.handle(setting(appearanceNS, colorSchemeKey, dbus.MakeVariant(Light)))
	assert.Equal(t, "light", w.Scheme())

	assert.Equal(t, []State{{Available: true, ColorScheme: Dark}, {Available: true, ColorScheme: Light}}, changes)
}

func TestFirstAnswerCountsAsAChangeEvenWithoutPreference(t *testing.T) {
	var changes []State
	w := &Watcher{onChange: func(s State) { changes = append(changes, s) }}
	w.update(dbus.MakeVariant(NoPreference))
	assert.Equal(t, []State{{Available: true, ColorScheme: NoPreference}}, changes)
	assert.Equal(t, "none", w.Scheme())
}

func TestStateWireShape(t *testing.T) {
	raw, err := json.Marshal(State{Available: true, ColorScheme: Light})
	require.NoError(t, err)
	assert.JSONEq(t, `{"available":true,"colorScheme":2}`, string(raw))
}
