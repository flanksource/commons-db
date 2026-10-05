// The kinds catalog: the trace plugins a host serves, by kind name, and the
// description of each a client builds its start form and result view from.

package traces

import (
	"fmt"
	"slices"
	"sync"

	"github.com/flanksource/clicky/rpc"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
)

// KindInfo describes one registered kind.
type KindInfo struct {
	Name         string             `json:"name"`
	Title        string             `json:"title"`
	Capabilities Capabilities       `json:"capabilities"`
	Params       *rpc.OpenAPISchema `json:"params"`
	Columns      []query.ColumnDef  `json:"columns"`
}

// Kinds is the trace plugins a host serves, by kind name.
type Kinds struct {
	mu      sync.RWMutex
	plugins map[string]TracePlugin
}

func NewKinds() *Kinds {
	return &Kinds{plugins: map[string]TracePlugin{}}
}

// RegisterKind serves plugin as kind name, refusing a plugin whose params or
// schema could not be described, and a name already taken.
func (k *Kinds) RegisterKind(name string, plugin TracePlugin) error {
	if err := recordstore.ValidateKind(name); err != nil {
		return err
	}
	if plugin.Title() == "" {
		return fmt.Errorf("trace kind %q needs a title", name)
	}
	if plugin.Capabilities() == (Capabilities{}) {
		return fmt.Errorf("trace kind %q declares no capabilities", name)
	}
	if _, err := plugin.Params(); err != nil {
		return fmt.Errorf("trace kind %q: %w", name, err)
	}
	if _, err := plugin.Schema(name); err != nil {
		return fmt.Errorf("trace kind %q: %w", name, err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.plugins[name]; ok {
		return fmt.Errorf("trace kind %q is already registered", name)
	}
	k.plugins[name] = plugin
	return nil
}

// Get finds the plugin registered as name.
func (k *Kinds) Get(name string) (TracePlugin, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	plugin, ok := k.plugins[name]
	return plugin, ok
}

// List describes every registered kind, by name.
func (k *Kinds) List() ([]KindInfo, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	infos := make([]KindInfo, 0, len(k.plugins))
	for _, name := range k.names() {
		plugin := k.plugins[name]
		params, err := plugin.Params()
		if err != nil {
			return nil, fmt.Errorf("trace kind %q: %w", name, err)
		}
		schema, err := plugin.Schema(name)
		if err != nil {
			return nil, fmt.Errorf("trace kind %q: %w", name, err)
		}
		infos = append(infos, KindInfo{
			Name: name, Title: plugin.Title(), Capabilities: plugin.Capabilities(), Params: params, Columns: schema.Columns,
		})
	}
	return infos, nil
}

// RegisterResultTypes declares every kind's result type into registry, which
// is how a results store opened with it (recordresults.OpenOptions.Register)
// knows the kinds' schemas and serves their <prefix>/<kind> profiles.
func (k *Kinds) RegisterResultTypes(registry *recordresults.Registry) error {
	k.mu.RLock()
	defer k.mu.RUnlock()
	for _, name := range k.names() {
		if err := k.plugins[name].register(registry, name); err != nil {
			return fmt.Errorf("trace kind %q: %w", name, err)
		}
	}
	return nil
}

func (k *Kinds) names() []string {
	names := make([]string, 0, len(k.plugins))
	for name := range k.plugins {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
