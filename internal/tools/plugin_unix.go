//go:build linux || darwin || freebsd

package tools

import (
	"fmt"
	"plugin"
)

// PluginRegister is the symbol signature expected from Go tool plugins.
type PluginRegister func(func(Tool, Handler) error) error

// LoadPlugin loads a Go plugin exporting RegisterEphemeraTools.
func LoadPlugin(path string) error {
	module, err := plugin.Open(path)
	if err != nil {
		return err
	}
	symbol, err := module.Lookup("RegisterEphemeraTools")
	if err != nil {
		return fmt.Errorf("plugin %s: missing RegisterEphemeraTools: %w", path, err)
	}
	register, ok := symbol.(func(func(Tool, Handler) error) error)
	if !ok {
		if named, namedOK := symbol.(PluginRegister); namedOK {
			register = named
		} else {
			return fmt.Errorf("plugin %s: RegisterEphemeraTools has incompatible signature", path)
		}
	}
	return register(Register)
}
