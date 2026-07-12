package schema

import (
	"fmt"
	"path"
	"strings"
)

// Bundle rewrites the external refs in source to local #/$defs refs and embeds
// the referenced component documents. refs is keyed by the relative ref used in
// source (for example "connections/postgres.json").
func Bundle(source Schema, refs map[string]Schema) (Schema, error) {
	root := cloneSchemaValue(source).(map[string]any)

	defs := Schema{}
	used := map[string]bool{}
	resolving := map[string]bool{}
	defSources := map[string]string{}
	var walk func(any) (any, error)
	var err error
	walk = func(value any) (any, error) {
		switch v := value.(type) {
		case []any:
			for i := range v {
				v[i], err = walk(v[i])
				if err != nil {
					return nil, err
				}
			}
			return v, nil
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok && !strings.HasPrefix(ref, "#") {
				if resolving[ref] {
					return nil, fmt.Errorf("cyclic schema ref %q", ref)
				}
				component, found := refs[ref]
				if !found {
					return nil, fmt.Errorf("unresolved schema ref %q", ref)
				}
				key := strings.TrimSuffix(path.Base(ref), path.Ext(ref))
				if previousRef, exists := defSources[key]; exists && previousRef != ref {
					return nil, fmt.Errorf("duplicate schema def key %q", key)
				}
				defSources[key] = ref
				if !used[ref] {
					def := cloneSchemaValue(component).(map[string]any)
					delete(def, "$schema")
					delete(def, "$id")
					resolving[ref] = true
					resolvedDef, resolveErr := walk(def)
					delete(resolving, ref)
					if resolveErr != nil {
						return nil, resolveErr
					}
					defs[key] = resolvedDef
					used[ref] = true
				}
				v["$ref"] = "#/$defs/" + key
			}
			for key, child := range v {
				v[key], err = walk(child)
				if err != nil {
					return nil, err
				}
			}
			return v, nil
		default:
			return value, nil
		}
	}

	result, err := walk(root)
	if err != nil {
		return nil, err
	}
	root = result.(map[string]any)
	if len(defs) > 0 {
		root["$defs"] = defs
	}
	return root, nil
}

func cloneSchemaValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			out[key] = cloneSchemaValue(child)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = cloneSchemaValue(child)
		}
		return out
	case []string:
		return append([]string(nil), v...)
	case map[string]string:
		out := make(map[string]string, len(v))
		for key, child := range v {
			out[key] = child
		}
		return out
	case map[string][]string:
		out := make(map[string][]string, len(v))
		for key, child := range v {
			out[key] = append([]string(nil), child...)
		}
		return out
	default:
		return value
	}
}
