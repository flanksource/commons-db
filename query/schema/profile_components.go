package schema

// providerTypeIcons are runtime icon names resolved by clicky-ui's fallback
// icon provider. They intentionally mirror the profile surface icon families.
var providerTypeIcons = map[string]string{
	"sql":           "database",
	"postgres":      "postgres",
	"mysql":         "mysql",
	"sqlserver":     "sqlserver",
	"clickhouse":    "clickhouse",
	"http":          "globe",
	"prometheus":    "prometheus",
	"postgrest":     "globe",
	"loki":          "grafana",
	"opensearch":    "opensearch",
	"opentelemetry": "opentelemetry",
	"jaeger":        "activity",
}

// ProfileComponents returns one standalone provider-form component per
// registered built-in profile provider.
func ProfileComponents() map[string]Schema {
	components := make(map[string]Schema, len(providerTypes))
	for _, typ := range providerTypes {
		components[typ] = Schema{
			"$schema": Draft,
			"$id":     typ + ".json",
			"title":   "Query provider: " + typ,
			"type":    "object",
			"required": []string{
				"type",
			},
			"properties": Schema{
				"type":       Schema{"type": "string", "title": "Type", "const": typ},
				"connection": connectionProp(),
				"options":    providerOptions(typ),
			},
		}
	}
	return components
}

func providerOptions(typ string) Schema {
	props := Schema{}
	switch typ {
	case "sql":
		props["type"] = Schema{
			"type":  "string",
			"title": "Driver",
			"enum":  []string{"postgres", "mysql", "sql_server", "clickhouse"},
			"x-enum-icons": map[string]string{
				"postgres": "postgres", "mysql": "mysql", "sql_server": "sqlserver", "clickhouse": "clickhouse",
			},
			"x-enum-display": "combobox",
		}
		props["url"] = inlineURLProp("URL / DSN", "Inline database URL used instead of a saved connection")
		props["database"] = strProp("Database", "Database override for this query")
	case "postgres", "mysql", "sqlserver", "clickhouse":
		props["url"] = inlineURLProp("URL / DSN", "Inline database URL used instead of a saved connection")
		props["database"] = strProp("Database", "Database override for this query")
	case "http":
		props["url"] = inlineURLProp("Base URL", "Inline HTTP base URL used instead of a saved connection")
		props["method"] = Schema{"type": "string", "title": "Method", "enum": []string{"GET", "POST", "PUT", "PATCH", "DELETE"}}
		props["body"] = Schema{"type": "string", "title": "Body", "format": "textarea"}
		props["jsonpath"] = strProp("JSONPath", "Extract rows from a wrapped JSON response")
	case "prometheus":
		props["url"] = inlineURLProp("URL", "Inline Prometheus URL used instead of a saved connection")
		props["range"] = Schema{
			"type":  "object",
			"title": "Range",
			"properties": Schema{
				"start": strProp("Start", "Date math, for example now-1h"),
				"end":   strProp("End", "Date math, for example now"),
				"step":  strProp("Step", "Sample step, for example 30s"),
			},
		}
		props["selectLabels"] = Schema{"type": "array", "title": "Select labels", "items": Schema{"type": "string"}}
	case "postgrest":
		props["url"] = inlineURLProp("Base URL", "Inline PostgREST base URL used instead of a saved connection")
		props["jsonpath"] = strProp("JSONPath", "Extract rows from a wrapped JSON response")
	case "loki":
		props["url"] = inlineURLProp("URL", "Inline Loki URL used instead of a saved connection")
		for _, field := range []string{"start", "end", "limit", "since", "step"} {
			props[field] = strProp(titleCase(field), "")
		}
		props["direction"] = Schema{"type": "string", "title": "Direction", "enum": []string{"forward", "backward"}}
	case "opensearch":
		props["address"] = inlineURLProp("Address", "Inline OpenSearch address used instead of a saved connection")
		props["index"] = strProp("Index", "Index or index pattern")
		props["limit"] = strProp("Limit", "Maximum number of hits")
	case "opentelemetry":
		for _, field := range []string{"format", "index", "dateField", "traceIdField", "spanIdField", "parentIdField", "parentRefType", "serviceField", "operationField"} {
			props[field] = strProp(titleCase(field), "")
		}
		for _, field := range []string{"statusFields", "selectFields", "sourceExcludes"} {
			props[field] = Schema{"type": "array", "title": titleCase(field), "items": Schema{"type": "string"}}
		}
		props["params"] = Schema{"type": "object", "title": "Provider Params"}
	case "jaeger":
		props["url"] = inlineURLProp("URL", "Inline Jaeger query URL used instead of a saved connection")
		for _, field := range []string{"service", "operation", "lookback", "start", "end", "limit", "minDuration", "maxDuration", "tags"} {
			props[field] = strProp(titleCase(field), "")
		}
	}
	return Schema{"type": "object", "title": "Options", "properties": props}
}

// BrowserOptions returns the provider-specific options form used when querying
// a saved connection. Endpoint and driver overrides are intentionally removed:
// a connection browser must remain scoped to the selected stored connection.
func BrowserOptions(typ string) Schema {
	options := providerOptions(typ)
	props, _ := options["properties"].(Schema)
	delete(props, "url")
	delete(props, "address")
	delete(props, "type")
	return options
}

func inlineURLProp(title, description string) Schema {
	return Schema{
		"type":                    "string",
		"title":                   title,
		"description":             description,
		"x-clicky-component":      "k8s-url-selector",
		"x-clicky-default-source": "value",
	}
}

func titleCase(value string) string {
	if value == "" {
		return value
	}
	for i := 1; i < len(value); i++ {
		if value[i] >= 'A' && value[i] <= 'Z' {
			return value[:i] + " " + value[i:]
		}
	}
	return string(value[0]-32) + value[1:]
}
