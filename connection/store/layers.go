// Package store resolves named connections from typed Connection resources
// (connection/crd) kept as files, in layers a later one of which overrides an
// earlier one by name: by default the connections every Flanksource tool
// shares, then an app's own, then the working directory's. A Store attached
// to a context (Attach) answers connection:// references before the
// connections table does.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Layer is a directory of connection files.
type Layer struct {
	// Name identifies the layer, such as "global", "app" or "cwd".
	Name string
	Dir  string
}

// Default layer names.
const (
	LayerGlobal = "global"
	LayerApp    = "app"
	LayerCWD    = "cwd"
)

// GlobalApp is the config dir name the connections every Flanksource tool
// shares live under.
const GlobalApp = "flanksource"

// ConfigDir is name's config dir: $XDG_CONFIG_HOME/name when that is set,
// otherwise ~/.config/name on every platform.
func ConfigDir(name string) (string, error) {
	if name == "" {
		return "", errors.New("config dir: a name is required")
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, name), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config dir: %w", err)
	}
	return filepath.Join(home, ".config", name), nil
}

// DefaultLayers are the connections every Flanksource tool shares
// (<config>/flanksource/connections), then app's own
// (<config>/<app>/connections), then the working directory's
// (./.connections), each overriding the one before.
func DefaultLayers(app string) ([]Layer, error) {
	if app == "" || app == GlobalApp {
		return nil, fmt.Errorf("connection layers: an app name other than %q is required, got %q", GlobalApp, app)
	}
	global, err := ConfigDir(GlobalApp)
	if err != nil {
		return nil, err
	}
	own, err := ConfigDir(app)
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("connection layers: %w", err)
	}
	return []Layer{
		{Name: LayerGlobal, Dir: filepath.Join(global, "connections")},
		{Name: LayerApp, Dir: filepath.Join(own, "connections")},
		{Name: LayerCWD, Dir: filepath.Join(cwd, ".connections")},
	}, nil
}
