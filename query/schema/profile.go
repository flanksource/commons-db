package schema

import (
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
)

// providerTypes are the registered query provider keys, used as the enum for the
// profile-setup form's provider.type field.
var providerTypes = []string{
	"sql", "postgres", "mysql", "sqlserver", "clickhouse",
	"http", "prometheus", "postgrest", "loki", "opensearch", "jaeger",
}

// providerConnectionTypes maps each profile provider type to the connection
// type(s) it can use, so the connection picker only offers compatible
// connections. The mapping mirrors the per-key connType registered in
// query/providers (e.g. the generic "sql" provider accepts any SQL backend). The
// connection lookup widget reads this off `x-clicky-connection-types` and sends
// the eligible types as a scope filter. Note ConnectionTypeSQLServer is
// "sql_server" — the value the connection list filters on.
var providerConnectionTypes = map[string][]string{
	"sql":        {models.ConnectionTypePostgres, models.ConnectionTypeMySQL, models.ConnectionTypeSQLServer, models.ConnectionTypeClickHouse},
	"postgres":   {models.ConnectionTypePostgres},
	"mysql":      {models.ConnectionTypeMySQL},
	"sqlserver":  {models.ConnectionTypeSQLServer},
	"clickhouse": {models.ConnectionTypeClickHouse},
	"http":       {models.ConnectionTypeHTTP},
	"postgrest":  {models.ConnectionTypeHTTP},
	"prometheus": {models.ConnectionTypePrometheus},
	"loki":       {models.ConnectionTypeLoki},
	"opensearch": {models.ConnectionTypeOpenSearch},
	"jaeger":     {models.ConnectionTypeJaeger},
}

// ProfileSource returns the externally referenced profile form schema. Each
// provider branch points at its standalone component under profiles/.
func ProfileSource() Schema {
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
		"type":            "object",
		"title":           "Provider",
		"required":        []string{"type"},
		"x-discriminator": "type",
		"properties": Schema{
			"type": Schema{
				"type":           "string",
				"title":          "Type",
				"enum":           providerTypes,
				"x-enum-icons":   providerTypeIcons,
				"x-enum-display": "combobox",
			},
			"connection": connectionProp(),
			"options":    Schema{"type": "object", "title": "Options"},
		},
	}
	for _, typ := range providerTypes {
		provider["allOf"] = append(providerAllOf(provider), Schema{
			"if": Schema{
				"properties": Schema{"type": Schema{"const": typ}},
				"required":   []string{"type"},
			},
			"then": Schema{"$ref": "profiles/" + typ + ".json"},
		})
	}

	return Schema{
		"$schema":  Draft,
		"title":    "Profile",
		"type":     "object",
		"required": []string{"profile", "provider"},
		"properties": Schema{
			"profile": strProp("Name", "Profile name"),
			"namespace": Schema{
				"type":               "string",
				"title":              "Namespace",
				"x-clicky-component": "k8s-namespace-selector",
				"x-clicky-order":     1,
			},
			"provider": provider,
			"query": Schema{
				"type":        "string",
				"title":       "Query",
				"format":      "textarea",
				"description": "Provider-native query; may reference {{.params.<name>}}",
			},
			"params":  Schema{"type": "array", "title": "Params", "items": paramDef},
			"columns": Schema{"type": "array", "title": "Columns", "items": columnDef},
			"output":  Schema{"type": "array", "title": "Output", "items": Schema{"type": "string"}},
			"render":  Schema{"type": "string", "title": "Render", "enum": []string{"table", "logs"}, "description": "Presentation mode: table (default) or logs (canonical LogsTable view for trace/log profiles)"},
		},
	}
}

func providerAllOf(provider Schema) []any {
	if allOf, ok := provider["allOf"].([]any); ok {
		return allOf
	}
	return nil
}

// Profile returns the bundled profile schema consumed by clicky-ui.
func Profile() Schema {
	refs := SchemaRefs("profiles", ProfileComponents())
	bundled, err := Bundle(ProfileSource(), refs)
	if err != nil {
		panic("bundle profile schema: " + err.Error())
	}
	return bundled
}

// connectionProp is the provider.connection field: an x-clicky-lookup entity
// picker over saved connections. The lookup fetches options from the connection
// list endpoint (server-side search), scoped to the connection types valid for
// the selected provider.type (scope.map). Single-select allows free-form entry so
// an inline DSN/URL still commits.
func connectionProp() Schema {
	return Schema{
		"type":        "string",
		"title":       "Connection",
		"description": "Pick a saved connection or type an inline DSN/URL",
		"x-clicky-lookup": Schema{
			"url":         "/api/v1/connection",
			"filter":      "connection",
			"searchParam": "__lookup_q",
			"multi":       false,
			"scope": Schema{
				"param": "types",
				"from":  "provider.type",
				"map":   providerConnectionTypes,
			},
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
	if p.Render != "" {
		s["x-clicky-render"] = p.Render
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
