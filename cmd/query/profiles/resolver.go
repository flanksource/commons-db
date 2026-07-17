package profiles

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/commons-db/query"
)

type ResolvedProfile struct {
	Profile           query.Profile
	ConnectionProfile string
}

func Resolve(ctx context.Context, store Store, name string) (ResolvedProfile, error) {
	return resolve(ctx, store, name, nil)
}

func resolve(ctx context.Context, store Store, name string, path []string) (ResolvedProfile, error) {
	if index := slices.Index(path, name); index >= 0 {
		return ResolvedProfile{}, fmt.Errorf("profile import cycle: %s", strings.Join(append(path[index:], name), " -> "))
	}
	current, err := store.Get(ctx, name)
	if err != nil {
		return ResolvedProfile{}, fmt.Errorf("resolve profile %q: %w", name, err)
	}
	if current.Name == "" {
		return ResolvedProfile{}, fmt.Errorf("resolve profile %q: profile not found", name)
	}

	var result ResolvedProfile
	for _, importedName := range current.Imports {
		imported, err := resolve(ctx, store, importedName, append(path, name))
		if err != nil {
			return ResolvedProfile{}, fmt.Errorf("profile %q imports %q: %w", current.Name, importedName, err)
		}
		result.Profile = mergeProfile(result.Profile, imported.Profile)
		if imported.Profile.Provider.Type != "" {
			result.ConnectionProfile = imported.ConnectionProfile
		}
	}

	previousType := result.Profile.Provider.Type
	result.Profile = mergeProfile(result.Profile, current)
	result.Profile.Name = current.Name
	result.Profile.Imports = nil
	if current.Provider.Connection != "" || current.Provider.Type != "" && current.Provider.Type != previousType {
		result.ConnectionProfile = current.Name
	}
	if result.ConnectionProfile == "" && result.Profile.Provider.Type != "" {
		result.ConnectionProfile = current.Name
	}
	return result, nil
}

func mergeProfile(base, overlay query.Profile) query.Profile {
	merged := base
	if overlay.Name != "" {
		merged.Name = overlay.Name
	}
	if overlay.Namespace != "" {
		merged.Namespace = overlay.Namespace
	}
	if overlay.Provider.Type != "" {
		merged.Provider.Type = overlay.Provider.Type
	}
	if overlay.Provider.Connection != "" {
		merged.Provider.Connection = overlay.Provider.Connection
	}
	merged.Provider.Options = mergeMap(merged.Provider.Options, overlay.Provider.Options)
	if overlay.Query != "" {
		merged.Query = overlay.Query
	}
	merged.Params = mergeParams(merged.Params, overlay.Params)
	if len(overlay.Columns) > 0 {
		merged.Columns = slices.Clone(overlay.Columns)
	}
	if len(overlay.Aliases) > 0 {
		merged.Aliases = slices.Clone(overlay.Aliases)
	}
	if len(overlay.Ignore) > 0 {
		merged.Ignore = slices.Clone(overlay.Ignore)
	}
	if len(overlay.Processors) > 0 {
		merged.Processors = slices.Clone(overlay.Processors)
	}
	if len(overlay.Context) > 0 {
		if merged.Context == nil {
			merged.Context = map[string]query.SubQuery{}
		}
		for name, subquery := range overlay.Context {
			merged.Context[name] = subquery
		}
	}
	if len(overlay.Output) > 0 {
		merged.Output = slices.Clone(overlay.Output)
	}
	if overlay.Render != "" {
		merged.Render = overlay.Render
	}
	if overlay.Trace != nil {
		merged.Trace = overlay.Trace
	}
	if overlay.Top != nil {
		merged.Top = overlay.Top
	}
	return merged
}

func mergeMap(base, overlay map[string]any) map[string]any {
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
			merged[key] = mergeMap(baseMap, overlayMap)
			continue
		}
		merged[key] = value
	}
	return merged
}

func mergeParams(base, overlay []query.ParamDef) []query.ParamDef {
	merged := slices.Clone(base)
	for _, incoming := range overlay {
		index := slices.IndexFunc(merged, func(existing query.ParamDef) bool { return existing.Name == incoming.Name })
		if index < 0 {
			merged = append(merged, incoming)
			continue
		}
		merged[index] = mergeParam(merged[index], incoming)
	}
	return merged
}

func mergeParam(base, overlay query.ParamDef) query.ParamDef {
	if overlay.Label != "" {
		base.Label = overlay.Label
	}
	if overlay.Type != "" {
		base.Type = overlay.Type
	}
	if overlay.Role != "" {
		base.Role = overlay.Role
	}
	if overlay.Default != nil {
		base.Default = overlay.Default
	}
	if len(overlay.Options) > 0 {
		base.Options = slices.Clone(overlay.Options)
	}
	base.Required = base.Required || overlay.Required
	if overlay.Description != "" {
		base.Description = overlay.Description
	}
	if overlay.Template != "" {
		base.Template = overlay.Template
	}
	return base
}
