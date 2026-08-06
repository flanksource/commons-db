package query

import (
	"fmt"
	"sort"
	"strings"
)

// NamedProcessor is a reusable, named ProcessorSpec preset — the processor
// equivalent of Profile.Imports. A profile references it as
// `processors: [{use: <name>}]` instead of restating the type and its whole
// configuration, and can still override individual config keys.
//
// Implementations register their presets alongside the processor itself, in the
// subpackage consumers blank-import.
type NamedProcessor struct {
	// Name is the library key referenced by ProcessorSpec.Use (e.g.
	// "java.stacktrace").
	Name string `json:"name" yaml:"name"`

	// Title is the human label shown in the profile editor.
	Title string `json:"title,omitempty" yaml:"title,omitempty"`

	// Description explains what the preset does to a result set.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Spec is the processor type and its default configuration.
	Spec ProcessorSpec `json:"spec" yaml:"spec"`
}

var namedProcessorRegistry = map[string]NamedProcessor{}

// RegisterNamedProcessor adds p to the global library, keyed by p.Name.
func RegisterNamedProcessor(p NamedProcessor) {
	namedProcessorRegistry[p.Name] = p
}

// GetNamedProcessor returns the library entry for name, or an error listing the
// available names.
func GetNamedProcessor(name string) (NamedProcessor, error) {
	entry, ok := namedProcessorRegistry[name]
	if !ok {
		return NamedProcessor{}, fmt.Errorf("no processor named %q in the library (available: %s)",
			name, strings.Join(NamedProcessorNames(), ", "))
	}
	return entry, nil
}

// NamedProcessorNames returns the library keys, sorted.
func NamedProcessorNames() []string {
	names := make([]string, 0, len(namedProcessorRegistry))
	for name := range namedProcessorRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// NamedProcessors returns every library entry, sorted by name. The profile
// schema uses this to offer the presets as an enum.
func NamedProcessors() []NamedProcessor {
	entries := make([]NamedProcessor, 0, len(namedProcessorRegistry))
	for _, name := range NamedProcessorNames() {
		entries = append(entries, namedProcessorRegistry[name])
	}
	return entries
}

// Resolve expands a library reference into a self-contained spec: the library
// entry's type, with the caller's config deep-merged over the preset's. A spec
// that names no library entry is returned unchanged.
func (s ProcessorSpec) Resolve() (ProcessorSpec, error) {
	if s.Use == "" {
		if s.Type == "" {
			return ProcessorSpec{}, fmt.Errorf("processor requires either type or use")
		}
		return s, nil
	}
	entry, err := GetNamedProcessor(s.Use)
	if err != nil {
		return ProcessorSpec{}, err
	}
	if s.Type != "" && s.Type != entry.Spec.Type {
		return ProcessorSpec{}, fmt.Errorf("processor %q is type %q, not %q; drop the type to use the library entry",
			s.Use, entry.Spec.Type, s.Type)
	}
	return ProcessorSpec{
		Type:   entry.Spec.Type,
		Use:    s.Use,
		Config: mergeProcessorConfig(entry.Spec.Config, s.Config),
	}, nil
}

// mergeProcessorConfig layers overlay over base one key at a time, recursing
// into nested objects, so overriding `set.message` keeps the preset's other
// `set` entries.
func mergeProcessorConfig(base, overlay map[string]any) map[string]any {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	merged := make(map[string]any, len(base)+len(overlay))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range overlay {
		baseMap, baseOK := merged[key].(map[string]any)
		overlayMap, overlayOK := value.(map[string]any)
		if baseOK && overlayOK {
			merged[key] = mergeProcessorConfig(baseMap, overlayMap)
			continue
		}
		merged[key] = value
	}
	return merged
}
