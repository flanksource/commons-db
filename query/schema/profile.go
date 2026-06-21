package schema

import "github.com/flanksource/commons-db/query"

// providerTypes are the registered query provider keys, used as the enum for the
// profile-setup form's provider.type field.
var providerTypes = []string{
	"sql", "postgres", "mysql", "sqlserver", "clickhouse",
	"http", "prometheus", "postgrest", "loki", "opensearch",
}

// Profile returns the profile-setup JSON Schema used to create/edit a Profile.
func Profile() Schema {
	paramDef := Schema{
		"type":     "object",
		"required": []string{"name"},
		"properties": Schema{
			"name":        strProp("Name", "Parameter key, referenced as {{.params.<name>}}"),
			"label":       strProp("Label", ""),
			"type":        Schema{"type": "string", "title": "Type", "enum": []string{"string", "number", "boolean", "date", "enum"}},
			"default":     Schema{"title": "Default"},
			"options":     Schema{"type": "array", "title": "Options", "items": Schema{"type": "string"}},
			"required":    Schema{"type": "boolean", "title": "Required"},
			"description": strProp("Description", ""),
			"template":    strProp("Template", "Value rewrite; {value} is the supplied value"),
		},
	}

	columnDef := Schema{
		"type":     "object",
		"required": []string{"name"},
		"properties": Schema{
			"name":   strProp("Name", ""),
			"label":  strProp("Label", ""),
			"type":   Schema{"type": "string", "title": "Type", "enum": []string{"string", "number", "boolean", "datetime", "duration", "bytes", "status", "health"}},
			"format": strProp("Format", ""),
			"unit":   strProp("Unit", ""),
			"width":  Schema{"type": "integer", "title": "Width"},
			"cel":    strProp("CEL", "Expression computing the cell value from `row`"),
			"hidden": Schema{"type": "boolean", "title": "Hidden"},
		},
	}

	provider := Schema{
		"type":     "object",
		"title":    "Provider",
		"required": []string{"type"},
		"properties": Schema{
			"type":       Schema{"type": "string", "title": "Type", "enum": providerTypes},
			"connection": strProp("Connection", "connection://name or an inline DSN/URL"),
			"options":    Schema{"type": "object", "title": "Options"},
		},
	}

	return Schema{
		"$schema":  Draft,
		"title":    "Profile",
		"type":     "object",
		"required": []string{"profile", "provider"},
		"properties": Schema{
			"profile":  strProp("Name", "Profile name"),
			"provider": provider,
			"query":    strProp("Query", "Provider-native query; may reference {{.params.<name>}}"),
			"params":   Schema{"type": "array", "title": "Params", "items": paramDef},
			"columns":  Schema{"type": "array", "title": "Columns", "items": columnDef},
			"output":   Schema{"type": "array", "title": "Output", "items": Schema{"type": "string"}},
		},
	}
}

// ProfileInstance returns a per-profile schema: the top-level `properties`
// describe the FilterBar inputs (from the profile's Params) and `x-clicky-columns`
// describes the DataTable (from the profile's Columns).
func ProfileInstance(p query.Profile) Schema {
	props := Schema{}
	var required []string
	for _, def := range p.Params {
		props[def.Name] = paramSchema(def)
		if def.Required {
			required = append(required, def.Name)
		}
	}

	columns := make([]any, 0, len(p.Columns))
	for _, c := range p.Columns {
		if c.Hidden {
			continue
		}
		col := Schema{
			"name":  c.Name,
			"label": labelOr(c.Label, c.Name),
		}
		if c.Type != "" {
			col["type"] = string(c.Type)
		}
		if c.Format != "" {
			col["format"] = c.Format
		}
		columns = append(columns, col)
	}

	s := Schema{
		"$schema":          Draft,
		"title":            p.Name,
		"type":             "object",
		"properties":       props,
		"x-clicky-columns": columns,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// paramSchema maps a ParamDef to its JSON Schema property for the FilterBar.
func paramSchema(def query.ParamDef) Schema {
	s := Schema{"title": def.DisplayLabel()}
	switch def.Type {
	case query.ParamTypeNumber:
		s["type"] = "number"
	case query.ParamTypeBoolean:
		s["type"] = "boolean"
	case query.ParamTypeDate:
		s["type"] = "string"
		s["format"] = "date-time"
	default:
		s["type"] = "string"
	}
	if len(def.Options) > 0 {
		s["enum"] = def.Options
	}
	if def.Default != nil {
		s["default"] = def.Default
	}
	if def.Description != "" {
		s["description"] = def.Description
	}
	return s
}
