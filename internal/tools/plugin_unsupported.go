//go:build !linux && !darwin && !freebsd

package tools

import "fmt"

// LoadPlugin reports that Go plugins are unavailable on this platform.
func LoadPlugin(path string) error {
	return fmt.Errorf("Go tool plugins are unsupported on this platform: %s", path)
}
