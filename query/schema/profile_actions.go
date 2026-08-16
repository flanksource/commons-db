package schema

func replaySpec() Schema {
	return Schema{
		"type": "object", "title": "Replay",
		"description": "Turn one result row back into an outbound HTTP request",
		"properties": Schema{
			"kind": Schema{
				"type": "string", "title": "Kind", "enum": []string{"http"}, "default": "http", "x-clicky-order": 1,
			},
			"target": Schema{
				"type": "object", "title": "Target",
				"description":    "Connection the request is sent to; required for a relative URL",
				"properties":     Schema{"connection": connectionProp(""), "url": strProp("URL", "Base URL")},
				"x-clicky-order": 2, "x-clicky-component": "connection-http",
			},
			"method": strProp("Method", `CEL expression yielding the HTTP method, e.g. "POST" (defaults to POST)`),
			"url":    strProp("URL", "CEL expression yielding an absolute URL or a path relative to the target"),
			"body": Schema{
				"type": "string", "title": "Body", "format": "textarea",
				"description": "CEL expression yielding the request body; non-string values are JSON encoded",
			},
			"headers": Schema{
				"type": "object", "title": "Headers",
				"description":          "Header name to CEL expression; an expression yielding blank omits the header",
				"additionalProperties": Schema{"type": "string"},
			},
		},
	}
}

func reconcileSpec() Schema {
	return Schema{
		"type": "object", "title": "Reconcile",
		"description": "Join this profile's rows against another profile on a shared identity",
		"properties": Schema{
			"dest": Schema{
				"type": "string", "title": "Destination profile", "description": "The profile to reconcile against",
				"x-clicky-order": 1, "x-clicky-lookup": profileRefLookup(false),
			},
			"key": Schema{
				"type": "object", "title": "Key",
				"description":    "How the shared identity is read from a row on either side; set columns or cel, never both",
				"x-clicky-order": 2,
				"properties": Schema{
					"columns": Schema{
						"type": "array", "title": "Columns", "items": Schema{"type": "string"},
						"description": "Row keys whose values, joined in order, form the key — only when both sides name them the same",
					},
					"cel": Schema{
						"type": "string", "title": "CEL", "format": "textarea",
						"description": "Expression evaluated against a row on either side; required when the two sides name the identity differently",
					},
				},
			},
			"timeColumn": strProp("Time column", "Row key holding each side's event time; defaults to the profile's timestamp column"),
			"range": Schema{
				"type": "object", "title": "Key range",
				"description": "Span of keys to reconcile; empty covers all of them. A range cuts both sides at the same keys, so a key missing from one side inside it is missing rather than merely unread",
				"properties": Schema{
					"from": strProp("From", "Reconcile keys at or after this one; empty starts at the first key"),
					"to":   strProp("To", "Reconcile keys before this one; empty runs to the last key"),
				},
			},
			"sourceFilters": Schema{
				"type": "object", "title": "Source filters", "description": "Filter values applied only to this source profile",
				"additionalProperties": Schema{"type": "string"},
			},
			"destFilters": Schema{
				"type": "object", "title": "Destination filters", "description": "Filter values applied only to the destination profile",
				"additionalProperties": Schema{"type": "string"},
			},
		},
	}
}
