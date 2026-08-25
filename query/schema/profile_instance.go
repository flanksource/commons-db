package schema

import "github.com/flanksource/commons-db/query"

// ProfileInstance returns a per-profile schema: the top-level properties
// describe FilterBar inputs and x-clicky-columns describes the DataTable.
func ProfileInstance(p query.Profile) (Schema, error) {
	props := Schema{}
	var required []string
	for _, def := range p.Params {
		props[def.Name] = paramSchema(def)
		if def.Required {
			required = append(required, def.Name)
		}
	}
	runtimeBindings, err := p.RuntimeFilterBindings()
	if err != nil {
		return nil, err
	}
	for _, binding := range runtimeBindings {
		props[binding.Key] = Schema{
			"type": "string", "title": binding.Label,
			"x-clicky-filter": Schema{
				"kind": string(binding.Kind), "lookup": binding.Lookup, "multi": binding.Multi,
			},
		}
	}
	bindings, err := p.ColumnFilterBindings()
	if err != nil {
		return nil, err
	}
	filterByColumn := make(map[string]query.ColumnFilterBinding, len(bindings))
	for _, binding := range bindings {
		if binding.Column != "" {
			filterByColumn[binding.Column] = binding
		}
	}
	columns := make([]any, 0, len(p.Columns))
	for _, c := range p.Columns {
		if c.Hidden {
			continue
		}
		col := Schema{"name": c.Name, "label": labelOr(c.Label, c.Name)}
		if binding, ok := filterByColumn[c.Name]; ok {
			filter := Schema{
				"key": binding.Key, "kind": string(binding.Kind), "multi": binding.Multi, "lookup": binding.Lookup,
			}
			if binding.Limit > 0 {
				filter["limit"] = binding.Limit
			}
			if len(binding.Options) > 0 {
				filter["options"] = binding.Options
			}
			col["filter"] = filter
		}
		if c.Type != "" {
			col["type"] = string(c.Type)
		}
		if c.Kind != "" {
			col["kind"] = string(c.Kind)
		}
		if c.Format != "" {
			col["format"] = c.Format
		}
		if c.Unit != "" {
			col["unit"] = c.Unit
		}
		columns = append(columns, col)
	}
	s := Schema{
		"$schema": Draft, "title": p.Name, "type": "object",
		"properties": props, "x-clicky-columns": columns,
	}
	if render := p.RenderMode(); render != "" {
		s["x-clicky-render"] = render
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s, nil
}

func paramSchema(def query.ParamDef) Schema {
	s := Schema{"title": def.DisplayLabel()}
	switch def.Type {
	case query.ParamTypeNumber:
		s["type"] = "number"
	case query.ParamTypeBoolean:
		s["type"] = "boolean"
	case query.ParamTypeDate:
		s["type"] = "string"
		s["format"] = "date"
	case query.ParamTypeDateTime:
		s["type"] = "string"
		s["format"] = "date-time"
	case query.ParamTypeList, query.ParamTypeLabels:
		s["type"] = "array"
		s["items"] = Schema{"type": "string"}
	default:
		s["type"] = "string"
	}
	if len(def.Options) > 0 {
		if def.Type == query.ParamTypeList || def.Type == query.ParamTypeLabels {
			s["items"] = Schema{"type": "string", "enum": def.Options}
		} else {
			s["enum"] = def.Options
		}
	}
	if def.Default != nil {
		s["default"] = def.Default
	}
	if def.Description != "" {
		s["description"] = def.Description
	}
	return s
}
