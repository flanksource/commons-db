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

	// ParamTypeList accepts several values at once. `params.<Name>` holds the
	// included values as a []string, which an esdsl multi-operand condition
	// (terms, ids) binds directly. A query template must join them, passing the
	// list first — `{{ join .params.ids "','" }}` — because rendering the
	// parameter bare would emit Go's `[a b c]`.
	ParamTypeList ParamType = "list"
)

// ParamRole assigns a profile parameter to a first-class table control. Filter
// is the default; limit/offset/cursor drive the pager and time-from/time-to are
// paired into the table's date-range control.
type ParamRole string

const (
	ParamRoleFilter   ParamRole = "filter"
	ParamRoleLimit    ParamRole = "limit"
	ParamRoleOffset   ParamRole = "offset"
	ParamRoleTimeFrom ParamRole = "time-from"
	ParamRoleTimeTo   ParamRole = "time-to"

	// ParamRoleCursor carries a keyset position into a query that pages by one.
	// It is the role that makes cursor paging reachable on a backend whose
	// query text this package will not rewrite: the server decodes the opaque
	// cursor and exposes its key values as `params.<name>.<column>`, so the
	// author writes their own resume predicate — `WHERE (created_at, id) >
	// ({{.params.cursor.created_at}}, {{.params.cursor.id}})` — against the
	// order they declared.
	ParamRoleCursor ParamRole = "cursor"
)

// ParamDef declares one server-side filter parameter of a Profile. Supplied
// values are validated and coerced against the declaration, then exposed under
// `params.<Name>` to everything the provider is handed — the query, the provider
// options and the connection — before it runs. This mirrors legacy trace-profile
// params.
type ParamDef struct {
	// Name is the parameter key, referenced as `{{.params.<Name>}}` (or
	// `$(.params.<Name>)`) in the query, in any provider option, or in the
	// connection.
	Name string `json:"name" yaml:"name"`

	// Label is the human-facing name for the FilterBar. Defaults to Name.
	Label string `json:"label,omitempty" yaml:"label,omitempty"`

	// Type drives validation/coercion. Defaults to string.
	Type ParamType `json:"type,omitempty" yaml:"type,omitempty"`

	// Role maps the parameter to a first-class table control. Empty defaults to
	// filter. A profile can rename the pager parameters by assigning limit and
	// offset roles; time-from/time-to form one server-backed date-range picker.
	Role ParamRole `json:"role,omitempty" yaml:"role,omitempty"`

	// Default is used when no value is supplied.
	Default any `json:"default,omitempty" yaml:"default,omitempty"`

	// Options enumerates the allowed values (an enum). When set, a supplied value
	// must be one of these. A list parameter validates every selected value
	// against them, and leaving them empty asks the provider for its distinct
	// values instead.
	Options []string `json:"options,omitempty" yaml:"options,omitempty"`

	// Field is the backend field an include/exclude selection binds to. Declaring
	// it makes a list parameter tri-state: a value prefixed with "!" excludes,
	// and both halves resolve into the same native filter clauses a column filter
	// produces. `params.<Name>` still carries only the includes, so a query
	// template and an esdsl multi-operand condition see a plain list either way.
	// Only a provider that applies native filters may declare it — Validate
	// rejects the rest, so an exclusion can never be silently dropped.
	Field string `json:"field,omitempty" yaml:"field,omitempty"`

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
// `params`. A tri-state list parameter also yields a native include/exclude
// clause, so an exclusion travels the same path a column filter's does rather
// than needing a second transport. Undeclared keys in supplied are ignored — the
// caller decides which request values map to params.
func resolveParams(defs []ParamDef, supplied map[string]any) (map[string]any, []ColumnFilterValue, error) {
	resolved := make(map[string]any, len(defs))
	var filters []ColumnFilterValue
	for _, def := range defs {
		if def.Name == "" {
			return nil, nil, fmt.Errorf("param declaration is missing a name")
		}

		raw, ok := supplied[def.Name]
		if !ok || isEmptyParam(raw) {
			switch {
			case def.Default != nil:
				raw = def.Default
			case def.Required:
				return nil, nil, fmt.Errorf("param %q is required", def.Name)
			default:
				continue
			}
		}

		if def.Type == ParamTypeList {
			include, exclude, err := def.coerceList(raw)
			if err != nil {
				return nil, nil, fmt.Errorf("param %q: %w", def.Name, err)
			}
			// A selection that reduces to nothing is the same as an absent one,
			// so a required list is not satisfied by whitespace alone.
			if len(include) == 0 && len(exclude) == 0 {
				if def.Required {
					return nil, nil, fmt.Errorf("param %q is required", def.Name)
				}
				continue
			}
			resolved[def.Name] = include
			if def.Field != "" {
				filters = append(filters, ColumnFilterValue{
					Key: def.Name, Field: def.Field, Kind: ColumnFilterKindTerms,
					Include: include, Exclude: exclude,
				})
			}
			continue
		}

		val, err := def.coerce(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("param %q: %w", def.Name, err)
		}
		if def.Template != "" {
			val = strings.ReplaceAll(def.Template, "{value}", fmt.Sprintf("%v", val))
		}
		resolved[def.Name] = val
	}
	return resolved, filters, nil
}

// coerceList decodes a multi-value selection. The wire form is the one column
// filters already use — comma-joined, "!" excludes — so the CLI, the query
// string and a JSON body all decode through a single implementation.
//
// A list param is always a value selection, whatever its tokens look like: a
// bounded parameter is already expressible as two numbers plus a query
// template, and a bound list's whole contract is that params.<name> is a list
// of values the query interpolates.
func (d ParamDef) coerceList(raw any) ([]string, []string, error) {
	selection, err := parseColumnFilterSelection(ColumnFilterKindTerms, raw)
	if err != nil {
		return nil, nil, err
	}
	include, exclude := selection.Include, selection.Exclude
	if len(exclude) > 0 && d.Field == "" {
		return nil, nil, fmt.Errorf(
			"excluding a value (!%s) requires a backend field; declare `field` on the parameter", exclude[0])
	}
	for _, values := range [][]string{include, exclude} {
		for i, value := range values {
			if len(d.Options) > 0 && !slices.Contains(d.Options, value) {
				return nil, nil, fmt.Errorf("value %q is not one of the allowed options %v", value, d.Options)
			}
			if d.Template != "" {
				values[i] = strings.ReplaceAll(d.Template, "{value}", value)
			}
		}
	}
	return include, exclude, nil
}

// paramRoles indexes the declared params by name, defaulting an unset role to
// filter so a provider never has to re-apply that default.
func paramRoles(defs []ParamDef) map[string]ParamRole {
	roles := make(map[string]ParamRole, len(defs))
	for _, def := range defs {
		if def.Role == "" {
			roles[def.Name] = ParamRoleFilter
			continue
		}
		roles[def.Name] = def.Role
	}
	return roles
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
	switch typed := v.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	case []string:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	default:
		return false
	}
}
