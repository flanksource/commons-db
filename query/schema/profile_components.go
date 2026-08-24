package schema

// ProviderTypeIcon returns the runtime icon name for a provider type, or "" for
// an unknown one. It is the single source of the provider→glyph mapping: the
// profile form reads it as x-enum-icons and the sidebar surface reads it through
// profiles.providerIcon, so a profile wears the same mark in both places.
func ProviderTypeIcon(providerType string) string {
	return providerTypeIcons[providerType]
}

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

	"cloudwatch":        "aws",
	"bigquery":          "google-cloud",
	"gcpcloudlogging":   "google-cloud",
	"k8s":               "kubernetes",
	"azureloganalytics": "azure",
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
				"connection": connectionProp(typ),
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
		props["search"] = searchSpecProp()
	case "opentelemetry":
		for _, field := range []string{"format", "index", "dateField", "traceIdField", "spanIdField", "parentIdField", "parentRefType", "serviceField", "operationField"} {
			props[field] = strProp(titleCase(field), "")
		}
		for _, field := range []string{"statusFields", "selectFields", "sourceExcludes"} {
			props[field] = Schema{"type": "array", "title": titleCase(field), "items": Schema{"type": "string"}}
		}
		props["search"] = searchSpecProp()
	case "jaeger":
		props["url"] = inlineURLProp("URL", "Inline Jaeger query URL used instead of a saved connection")
		for _, field := range []string{"service", "operation", "lookback", "start", "end", "limit", "minDuration", "maxDuration", "tags"} {
			props[field] = strProp(titleCase(field), "")
		}
	case "cloudwatch":
		props["endpoint"] = inlineURLProp("Endpoint", "Inline CloudWatch endpoint used instead of the region's AWS endpoint")
		props["logGroup"] = strProp("Log group", "Log group the Insights query runs against")
		props["region"] = strProp("Region", "Overrides the region on the connection")
		props["start"] = strProp("Start", "Date math, for example now-1h. Defaults to now-1h — Insights requires a bounded range")
		props["end"] = strProp("End", "Date math, for example now")
		props["limit"] = strProp("Limit", "Maximum number of log lines")
		props["mapping"] = fieldMappingProp()
	case "gcpcloudlogging":
		props["project"] = strProp("Project", "Overrides the project on the connection")
		props["start"] = strProp("Start", "Date math, for example now-1h")
		props["end"] = strProp("End", "Date math, for example now")
		props["limit"] = strProp("Limit", "Maximum number of log entries")
		props["mapping"] = fieldMappingProp()
	case "bigquery":
		props["project"] = strProp("Project", "Overrides the project on the connection")
		// No start/end/limit: the BigQuery request carries only a query, so a
		// time range has to be written into the SQL itself.
		props["mapping"] = fieldMappingProp()
	case "k8s":
		props["kind"] = Schema{
			"type": "string", "title": "Kind",
			"description": "Workload to read logs from; a workload resolves to its current pods",
			"enum":        []string{"Pod", "Deployment", "StatefulSet", "DaemonSet"},
		}
		props["apiVersion"] = strProp("API version", "")
		props["namespace"] = strProp("Namespace", "Namespace of the workload")
		props["name"] = strProp("Name", "Name of the workload")
		props["pods"] = Schema{
			"type": "array", "title": "Pods",
			"description": "Resource selectors narrowing which of the workload's pods are read",
			"items":       Schema{"type": "object"},
		}
		props["containers"] = Schema{
			"type": "array", "title": "Containers",
			"description": "Match expressions picking which containers of each pod are read",
			"items":       Schema{"type": "string"},
		}
		props["start"] = strProp("Start", "Date math, for example now-1h")
		props["end"] = strProp("End", "Date math, for example now")
		props["limit"] = strProp("Limit", "Maximum number of log lines")
	case "azureloganalytics":
		props["workspaceID"] = strProp("Workspace ID", "Log Analytics workspace the KQL query runs against")
		props["start"] = strProp("Start", "Date math, for example now-1h")
		props["end"] = strProp("End", "Date math, for example now")
		props["limit"] = strProp("Limit", "Maximum number of rows")
		props["mapping"] = fieldMappingProp()
	}
	return Schema{"type": "object", "title": "Options", "properties": props}
}

// fieldMappingProp is the logs.FieldMappingConfig form: which source columns
// carry each canonical log field. Each is a list because backends disagree on
// the name and the first one present wins.
func fieldMappingProp() Schema {
	props := Schema{}
	for _, field := range []string{"id", "message", "timestamp", "host", "severity", "source", "ignore"} {
		props[field] = Schema{
			"type": "array", "title": titleCase(field),
			"items": Schema{"type": "string"},
		}
	}
	return Schema{
		"type": "object", "title": "Field mapping",
		"description": "Maps source columns onto the canonical log fields",
		"properties":  props,
	}
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
	// endpoint redirects an AWS client the same way url does elsewhere, so it
	// belongs on the same list — a browser must stay pointed at the connection
	// the user picked.
	delete(props, "endpoint")
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
