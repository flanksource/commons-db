package schema

import "github.com/flanksource/commons-db/query"

func profileSpansSchema() Schema {
	properties := Schema{}
	for index, name := range []string{"traceId", "spanId", "parentId", "startedAt", "durationMs", "service", "operation", "status", "kind", "attributes", "resource"} {
		fields := Schema{
			"source":   strProp("Source field", "Exact source key or nested field path"),
			"cel":      Schema{"type": "string", "title": "CEL", "format": "textarea", "description": "Expression evaluated against the source row before normalization"},
			"jsonpath": strProp("JSONPath", "Path rooted at the source row, or Source field when set"),
		}
		if name == "startedAt" || name == "durationMs" {
			fields["unit"] = Schema{"type": "string", "title": "Source unit", "enum": []string{"s", "ms", "us", "ns"}, "description": "Numeric timestamp or duration unit; canonical durations are milliseconds"}
		}
		fields["source"].(Schema)["x-clicky-order"] = 0
		fields["cel"].(Schema)["x-clicky-order"] = 2
		fields["jsonpath"].(Schema)["x-clicky-order"] = 3
		properties[name] = Schema{"type": "object", "title": titleCase(name), "x-clicky-order": index, "additionalProperties": false, "properties": fields}
	}
	return Schema{"type": "object", "title": "Canonical span mappings", "description": "Omit a mapping to detect it automatically", "additionalProperties": false, "properties": properties, "x-clicky-default-columns": query.DefaultSpanColumns()}
}
