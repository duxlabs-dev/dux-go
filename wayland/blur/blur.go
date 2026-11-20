// Package blur probes whether the compositor offers background blur.
package blur

import "github.com/duxlabs-dev/dux-go/wayland/client"

const managerInterface = "ext_background_effect_manager_v1"

// Supported reports whether the compositor advertises ext-background-effect-v1.
func Supported() (bool, error) {
	display, err := client.Connect("")
	if err != nil {
		return false, err
	}
	defer display.Context().Close()

	registry, err := display.GetRegistry()
	if err != nil {
		return false, err
	}
	found := false
	registry.SetGlobalHandler(func(e client.RegistryGlobalEvent) {
		if e.Interface == managerInterface {
			found = true
		}
	})
	if err := display.Roundtrip(); err != nil {
		return false, err
	}
	return found, nil
}
