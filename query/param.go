package query

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ParamType is the declared type of a Profile parameter. It drives validation,
// coercion of incoming (string) values, and the per-profile JSON schema.
type ParamType string

const (
	ParamTypeString  ParamType = "string"
	ParamTypeNumber  ParamType = "number"
	ParamTypeBoolean ParamType = "boolean"
	ParamTypeDate    ParamType = "date"
	ParamTypeEnum    ParamType = "enum"
)

// ParamDef declares one server-side filter parameter of a Profile. Supplied
// values are validated and coerced against the declaration, then exposed to the
// query template under `params.<Name>` before the provider runs. This mirrors
// legacy trace-profile params.
type ParamDef struct {
	// Name is the parameter key, referenced in the query as `{{.params.<Name>}}`.
	Name string `json:"name" yaml:"name"`

	// Label is the human-facing name for the FilterBar. Defaults to Name.
	Label string `json:"label,omitempty" yaml:"label,omitempty"`

	// Type drives validation/coercion. Defaults to string.
	Type ParamType `json:"type,omitempty" yaml:"type,omitempty"`

	// Default is used when no value is supplied.
	Default any `json:"default,omitempty" yaml:"default,omitempty"`

	// Options enumerates the allowed values (an enum). When set, a supplied value
	// must be one of these.
	Options []string `json:"options,omitempty" yaml:"options,omitempty"`

	// Required fails execution when no value (and no Default) is supplied.
	Required bool `json:"required,omitempty" yaml:"required,omitempty"`

	// Description is shown as the FilterBar tooltip.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Template optionally rewrites the resolved value; "{value}" is replaced with
	// the supplied value (e.g. "{value}-api").
	Template string `json:"template,omitempty" yaml:"template,omitempty"`
}

// DisplayLabel returns the Label when set, otherwise the Name.
func (d ParamDef) DisplayLabel() string {
	if d.Label != "" {
		return d.Label
	}
	return d.Name
}

// resolveParams validates and coerces the supplied values against the declared
// params, applies defaults and per-param templates, and enforces Required. The
// returned map (keyed by param name) is exposed to the query template as
// `params`. Undeclared keys in supplied are ignored — the caller decides which
// request values map to params.
func resolveParams(defs []ParamDef, supplied map[string]any) (map[string]any, error) {
	resolved := make(map[string]any, len(defs))
	for _, def := range defs {
		if def.Name == "" {
			return nil, fmt.Errorf("param declaration is missing a name")
		}

		raw, ok := supplied[def.Name]
		if !ok || isEmptyParam(raw) {
			switch {
			case def.Default != nil:
				raw = def.Default
			case def.Required:
				return nil, fmt.Errorf("param %q is required", def.Name)
			default:
				continue
			}
		}

		val, err := def.coerce(raw)
		if err != nil {
			return nil, fmt.Errorf("param %q: %w", def.Name, err)
		}
		if def.Template != "" {
			val = strings.ReplaceAll(def.Template, "{value}", fmt.Sprintf("%v", val))
		}
		resolved[def.Name] = val
	}
	return resolved, nil
}

// coerce converts a raw value to the param's declared type and validates it
// against Options, failing fast on a type mismatch or a value outside the enum.
func (d ParamDef) coerce(raw any) (any, error) {
	var val any
	switch d.Type {
	case "", ParamTypeString, ParamTypeEnum:
		val = fmt.Sprintf("%v", raw)
	case ParamTypeDate:
		s := fmt.Sprintf("%v", raw)
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			return nil, fmt.Errorf("value %q is not an RFC3339 date: %w", s, err)
		}
		val = s
	case ParamTypeNumber:
		switch v := raw.(type) {
		case float64, float32, int, int32, int64:
			val = v
		default:
			f, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprintf("%v", raw)), 64)
			if err != nil {
				return nil, fmt.Errorf("value %q is not a number", raw)
			}
			val = f
		}
	case ParamTypeBoolean:
		switch v := raw.(type) {
		case bool:
			val = v
		default:
			b, err := strconv.ParseBool(strings.TrimSpace(fmt.Sprintf("%v", raw)))
			if err != nil {
				return nil, fmt.Errorf("value %q is not a boolean", raw)
			}
			val = b
		}
	default:
		return nil, fmt.Errorf("unknown param type %q", d.Type)
	}

	if len(d.Options) > 0 {
		s := fmt.Sprintf("%v", val)
		if !slices.Contains(d.Options, s) {
			return nil, fmt.Errorf("value %q is not one of the allowed options %v", s, d.Options)
		}
	}
	return val, nil
}

func isEmptyParam(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && s == ""
}
