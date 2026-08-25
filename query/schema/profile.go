package schema

import (
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
)

// The params editor renders as a summary-row accordion, so a parameter's type
// and role each need a glyph the reader can recognise before reading a word.
// Names are clicky-ui icon consumerNames; tones are its FieldTone vocabulary.
// Both maps MUST be paired with an explicit x-enum-display: adding x-enum-icons
// alone flips an enum to clicky-ui's icon-card grid, which is far too wide for
// an accordion body.
var (
	paramTypeIcons = map[string]string{
		"string": "cursor-text", "number": "sigma", "boolean": "toggle-on",
		"date": "calendar", "datetime": "clock", "duration": "timer",
		"enum": "tag", "identifier": "database", "list": "list-dashes", "labels": "tags",
	}
	paramTypeTones = map[string]string{
		"string": "slate", "number": "violet", "boolean": "amber",
		"date": "sky", "datetime": "rose", "duration": "neutral",
		"enum": "teal", "identifier": "sky", "list": "indigo", "labels": "emerald",
	}
	paramRoleIcons = map[string]string{
		"filter": "filter", "limit": "list-ordered", "offset": "arrow-right",
		"time-from": "clock", "time-to": "timer",
	}
	// The role says what the parameter does to the query; the verb phrase reads
	// better than the bare code in the collapsed row's chip.
	paramRoleLabels = map[string]string{
		"filter": "filters", "limit": "row limit", "offset": "offset",
		"time-from": "time from", "time-to": "time to",
	}
)

// providerTypes are the registered query provider keys, used as the enum for the
// profile-setup form's provider.type field.
var providerTypes = []string{
	"sql", "postgres", "mysql", "sqlserver", "clickhouse",
	"http", "prometheus", "postgrest", "loki", "opensearch", "jaeger",
	"opentelemetry", "cloudwatch", "gcpcloudlogging", "bigquery", "k8s",
	"azureloganalytics",
}

// providerConnectionTypes maps each profile provider type to the connection
// type(s) it can use, so the connection picker only offers compatible
// connections. The mapping mirrors the per-key connType registered in
// query/providers (e.g. the generic "sql" provider accepts any SQL backend). The
// connection lookup widget reads this off `x-clicky-connection-types` and sends
// the eligible types as a scope filter. Note ConnectionTypeSQLServer is
// "sql_server" — the value the connection list filters on.
var providerConnectionTypes = map[string][]string{
	"sql":           {models.ConnectionTypePostgres, models.ConnectionTypeMySQL, models.ConnectionTypeSQLServer, models.ConnectionTypeClickHouse},
	"postgres":      {models.ConnectionTypePostgres},
	"mysql":         {models.ConnectionTypeMySQL},
	"sqlserver":     {models.ConnectionTypeSQLServer},
	"clickhouse":    {models.ConnectionTypeClickHouse},
	"http":          {models.ConnectionTypeHTTP},
	"postgrest":     {models.ConnectionTypeHTTP},
	"prometheus":    {models.ConnectionTypePrometheus},
	"loki":          {models.ConnectionTypeLoki},
	"opensearch":    {models.ConnectionTypeOpenSearch, models.ConnectionTypeElasticSearch},
	"opentelemetry": {models.ConnectionTypeOpenTelemetry},
	"jaeger":        {models.ConnectionTypeJaeger},

	"cloudwatch":        {models.ConnectionTypeAWS},
	"bigquery":          {models.ConnectionTypeGCP},
	"gcpcloudlogging":   {models.ConnectionTypeGCP},
	"k8s":               {models.ConnectionTypeKubernetes},
	"azureloganalytics": {models.ConnectionTypeAzure},
}

// ProfileSource returns the externally referenced profile form schema. Each
// provider branch points at its standalone component under profiles/.
func ProfileSource() Schema {
	paramDef := Schema{
		"type": "object",
		// The noun behind the list's "Add <noun>" row.
		"title":    "Parameter",
		"required": []string{"name"},
		// An enum parameter's options are answerable from the index it filters,
		// which the editor resolves through the condition that binds the parameter.
		"x-clicky-component": "es-param",
		// An expanded parameter flows into as many ~15rem columns as the pane
		// allows rather than one 600px stack, capped so ten fields never spread
		// into a single unreadable row on a wide screen.
		"x-columns":           "auto",
		"x-columns-max-width": "76rem",
		"properties": Schema{
			"name": Schema{
				"type": "string", "title": "Name", "x-clicky-order": 0,
				"description": "Parameter key, referenced as {{.params.<name>}} in the query, in any provider option, or in the connection",
			},
			"label": Schema{
				"type": "string", "title": "Label", "x-clicky-order": 1,
				"description": "Human-facing name in the filter bar; blank uses Name",
			},
			"type": Schema{
				"type": "string", "title": "Type", "x-clicky-order": 2,
				"enum":           []string{"string", "number", "boolean", "date", "datetime", "duration", "enum", "identifier", "list", "labels"},
				"x-enum-labels":  map[string]string{"datetime": "Date & time", "duration": "Duration", "identifier": "SQL identifier", "list": "List (multi-select)", "labels": "Kubernetes labels"},
				"x-enum-icons":   paramTypeIcons,
				"x-enum-tones":   paramTypeTones,
				"x-enum-display": "combobox",
				"description":    "Date resolves to YYYY-MM-DD; date & time resolves to RFC3339; duration resolves to whole milliseconds; identifier validates and quotes a SQL database, schema, table, or column name; list accepts several values; labels binds a Kubernetes labels.<key> field",
			},
			"role": Schema{
				"type": "string", "title": "Role", "x-clicky-order": 3,
				"enum":           []string{"filter", "limit", "offset", "time-from", "time-to"},
				"x-enum-labels":  paramRoleLabels,
				"x-enum-icons":   paramRoleIcons,
				"x-enum-display": "combobox",
				"description":    "Map this parameter to filtering, paging, or a date-range edge; cursors are provider-owned transport values",
			},
			"field": Schema{
				"type": "string", "title": "Field", "x-clicky-order": 4,
				"description":        "Backend field this parameter filters on; Kubernetes label parameters use labels.<key>",
				"x-clicky-component": "es-param-field",
			},
			"default":  Schema{"title": "Default", "x-clicky-order": 5},
			"template": Schema{"type": "string", "title": "Template", "x-clicky-order": 6, "description": "Value rewrite; {value} is the supplied value"},
			"required": Schema{"type": "boolean", "title": "Required", "x-clicky-order": 7},
			"options": Schema{
				"type": "array", "title": "Options", "items": Schema{"type": "string"},
				"x-clicky-order": 8, "x-col-span": "full",
				"description": "Allowed values; leave empty on a bound list to offer the backend's own distinct values",
			},
			"description": Schema{
				"type": "string", "title": "Description", "x-clicky-order": 9, "x-col-span": "full",
				"description": "Shown as the parameter's tooltip in the filter bar",
			},
		},
	}

	orderBy := Schema{
		"type":     "object",
		"title":    "Order column",
		"required": []string{"column"},
		"properties": Schema{
			"column": Schema{"type": "string", "title": "Column", "x-clicky-order": 0, "description": "Row key to order by"},
			"desc":   Schema{"type": "boolean", "title": "Descending", "x-clicky-order": 1},
			"unique": Schema{
				"type": "boolean", "title": "Unique", "x-clicky-order": 2,
				"description": "This column breaks all remaining ties, making the order total. Must be the last entry, and is what a cursor resumes from",
			},
		},
	}

	columnDef := Schema{
		"type":     "object",
		"required": []string{"name"},
		"properties": Schema{
			"label": Schema{"type": "string", "title": "Label", "description": "Optional column header; blank uses Name", "x-clicky-order": 0},
			"name":  Schema{"type": "string", "title": "Name", "description": "Public field name used by tables, filters, APIs, and exports", "x-clicky-order": 1},
			"source": Schema{
				"type": "string", "title": "Source", "description": "Original provider field copied into Name and removed from output", "x-clicky-order": 2,
			},
			"type": Schema{
				"type":           "string",
				"title":          "Type",
				"enum":           query.ColumnTypeValues(),
				"description":    "Value shape and default formatting; independent of Role",
				"x-clicky-order": 3,
				"x-enum-labels": map[string]string{
					"key_value":  "KeyValue{}",
					"key_values": "[]KeyValue",
					"json":       "JSON",
					"uuid":       "UUID",
				},
			},
			"kind": Schema{
				"type": "string", "title": "Role", "enum": []string{"timestamp", "tags", "status"},
				"description":    "Optional table behavior, independent of Type: timestamp adds the date-range control, tags renders filterable chips, and status applies status styling",
				"x-clicky-order": 4,
			},
			"format": Schema{
				"type": "string", "title": "Format", "enum": api.ColumnFormatValues(),
				"description":    "Optional display formatter; blank derives from Type and Unit takes precedence",
				"x-clicky-order": 5,
				"x-enum-labels":  map[string]string{"date": "Date/time", "float": "Number", "duration": "Duration", "bytes": "Bytes", "currency": "Currency"},
			},
			"unit": Schema{
				"type": "string", "title": "Unit", "enum": api.ColumnUnitValues(),
				"description":    "Numeric scaling and display unit; requires Type number, duration, or bytes and takes precedence over Format",
				"x-clicky-order": 6,
				"x-enum-labels": map[string]string{
					"none": "Compact count", "short": "Short number", "percent": "Percent (0-100)", "percentunit": "Percent (0-1)",
					"bytes": "Bytes (IEC)", "decbytes": "Bytes (SI)", "Bps": "Bytes/sec", "binBps": "Binary bytes/sec", "ms": "Milliseconds", "s": "Seconds",
				},
			},
			"width": Schema{"type": "integer", "title": "Width", "description": "Maximum rendered width in characters", "x-clicky-order": 7},
			"cel": Schema{
				"type": "string", "title": "CEL",
				"description":        "Expression computing the cell value from `row`",
				"x-clicky-component": celEditorComponent,
				"x-clicky-cel-scope": string(query.ScopeRow),
				"x-clicky-order":     8,
			},
			"jsonpath": Schema{
				"type": "string", "title": "JSONPath",
				"description":        "Path computing the cell value, rooted at the row or at Source when it is set; an alternative to CEL",
				"x-clicky-component": "jsonpath-picker",
				"x-clicky-order":     9,
			},
			"filter": Schema{
				"type": "object", "title": "Filter", "x-clicky-order": 10,
				"description": "How this column is filtered at the backend; a direct column or simple CEL infers the field, and Type infers the control",
				"properties": Schema{
					"field": Schema{
						"type": "string", "title": "Field",
						"description":        "Backend field the selection applies to — the indexed field for a document store, the result column for SQL; required only when the column implies none",
						"x-clicky-component": "es-param-field",
					},
					"nested": Schema{
						"type": "string", "title": "Nested",
						"description":        "The `nested` mapping the field lives inside; a selection on such a field is compiled inside a nested query, and a flat clause on one matches nothing",
						"x-clicky-component": "es-param-field",
					},
					"where": Schema{
						"type": "object", "title": "Where", "x-col-span": "full",
						"additionalProperties": Schema{"type": "string"},
						"description":          "Constants the selection also requires, keyed by backend field — the key of a key/value tag list, whose value is what the operator picks; requires Nested",
					},
					"kind": Schema{
						"type": "string", "title": "Kind", "enum": query.ColumnFilterKindValues(),
						"x-enum-labels": map[string]string{
							"terms":    "Value list",
							"exact":    "Exact match",
							"text":     "Text search",
							"range":    "Numeric range",
							"duration": "Duration range",
							"date":     "Date range",
							"time":     "Date & time range",
							"boolean":  "Boolean",
							"none":     "Not filterable",
						},
						"x-enum-display": "combobox",
						"description":    "Overrides the control derived from Type; set it where the rendered type and the backend storage disagree",
					},
					"options": Schema{
						"type": "array", "items": Schema{"type": "string"}, "title": "Options", "x-col-span": "full",
						"description": "Selectable values; leave empty to ask the backend for its own distinct values. Value selections only",
					},
					"lookup": Schema{
						"type": "boolean", "title": "Lookup", "default": true,
						"description": "Ask the backend for this field's distinct values; turn off for a high-cardinality field that is typed rather than picked. Value selections only",
					},
					"limit": Schema{
						"type": "integer", "title": "Limit", "default": query.DefaultFilterLookupLimit,
						"minimum": 1, "maximum": query.MaxFilterLookupLimit,
						"description": "How many distinct values the control offers before the rest have to be typed for; requires Lookup. Value selections only",
					},
					"multi": Schema{
						"type": "boolean", "title": "Multi", "default": true,
						"description": "Allow several values at once",
					},
					"disabled": Schema{
						"type": "boolean", "title": "Disabled",
						"description": "Offer no filter for this column while keeping the column itself rendered",
					},
				},
			},
			"hidden": Schema{"type": "boolean", "title": "Hidden", "description": "Hide the column from default output while retaining it for later column and style CEL", "x-clicky-order": 11},
			"style": Schema{
				"type": "string", "title": "Style",
				"description":        `CEL returning this cell's presentation classes, e.g. level == "ERROR" ? "text-red-500" : ""`,
				"x-clicky-component": celEditorComponent,
				"x-clicky-cel-scope": string(query.ScopeRow),
				"x-clicky-order":     12,
			},
		},
	}
	aliasDef := Schema{
		"type": "object", "required": []string{"name", "cel"},
		"properties": Schema{
			"name": strProp("Name", "Dotted output path"),
			"cel": Schema{
				"type": "string", "title": "CEL", "description": "Ordered row projection",
				"x-clicky-component": celEditorComponent,
				"x-clicky-cel-scope": string(query.ScopeRow),
			},
		},
	}
	filterDef := Schema{
		"type": "object", "required": []string{"fields"},
		"properties": Schema{
			"name":        strProp("Name", "Filter identity, shown in the UI"),
			"description": strProp("Description", "What this filter trims, for the tooltip"),
			"fields": Schema{
				"type": "object", "title": "Fields",
				"description":          "CEL predicates keyed by label, AND-ed together",
				"additionalProperties": Schema{"type": "string"},
			},
			"exclude": Schema{"type": "boolean", "title": "Exclude", "description": "Drop matching rows instead of keeping them"},
			"hidden":  Schema{"type": "boolean", "title": "Hidden", "description": "Always applied, rather than offered as a toggle"},
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
			"connection": connectionProp(""),
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
			// The dotted naming convention IS the import graph — `jms.incoming`
			// imports `jms` — so parents are picked from the same tree the
			// sidebar shows rather than typed from memory.
			"imports": Schema{
				"type": "array", "title": "Imports",
				"description":     "Profiles merged left to right before this one runs",
				"items":           Schema{"type": "string"},
				"x-clicky-lookup": profileRefLookup(true),
			},
			"icon": Schema{
				"type":           "string",
				"title":          "Icon",
				"description":    "Overrides the glyph shown in the sidebar and pickers; blank uses the provider's own mark",
				"x-clicky-order": 2,
			},
			"namespace": Schema{
				"type":               "string",
				"title":              "Namespace",
				"x-clicky-component": "k8s-namespace-selector",
				"x-clicky-order":     1,
			},
			"provider": provider,
			"query": Schema{
				"type":               "string",
				"title":              "Query",
				"format":             "textarea",
				"description":        "Provider-native query; may reference {{.params.<name>}}",
				"x-clicky-component": "profile-query-builder",
			},
			// Ten properties per parameter stacked in full costs ~700px of screen
			// each, and nothing in the list says which parameter is which. The
			// accordion collapses each to one scannable row; `x-item` names the
			// item PROPERTIES that identify it, so clicky-ui stays domain-agnostic.
			// The description doubles as the zero-item explanation in the add row.
			"params": Schema{
				"type": "array", "title": "Params", "items": paramDef,
				"description":        "A parameter turns a fixed query into a reusable one: give it a name and it becomes {{.params.<name>}} inside the query, the provider options and the connection.",
				"x-array-display":    "accordion",
				"x-clicky-component": "es-params",
				"x-item": Schema{
					// Mirrors ParamDef.DisplayLabel(): Label when set, else Name.
					"title":      []string{"label", "name"},
					"fallback":   "Untitled parameter",
					"summary":    []any{Schema{"property": "name", "pattern": "{{.params.{}}}"}, Schema{"property": "field"}},
					"glyph":      "type",
					"badge":      "role",
					"flag":       "required",
					"noun":       "parameter",
					"nounPlural": "parameters",
				},
			},
			"columns": Schema{"type": "array", "title": "Columns", "x-layout": "table", "items": columnDef},
			"aliases": Schema{"type": "array", "title": "Aliases", "x-layout": "table", "items": aliasDef},
			"ignore":  Schema{"type": "array", "title": "Ignore", "items": Schema{"type": "string"}},
			"filters": Schema{"type": "array", "title": "Filters", "x-array-display": "accordion", "items": filterDef},
			"order": Schema{
				"type": "array", "title": "Order", "x-layout": "table", "items": orderBy,
				"description": "Total order the rows are returned in, ending in a column marked unique. Paging past the first page requires it: without a tiebreaker, two runs of the same query may interleave tied rows differently and a second page can repeat or skip rows from the first.",
			},
			"processors": Schema{
				"type": "array", "title": "Processors", "x-layout": "table", "items": processorSpec(),
				"description": "Raw-row steps applied in order before aliases, filters, columns, and styles",
				// The order of this array is semantic — a stack-trace merge has to
				// run before a dedupe, or every frame dedupes as its own line — and
				// `config` is an untyped object no schema-driven control can check.
				// The pipeline widget owns both, so it replaces the table layout.
				"x-clicky-component": "processor-pipeline",
				"x-clicky-presets":   namedProcessorPresets(),
			},
			"trace":     traceSpec(),
			"replay":    replaySpec(),
			"reconcile": reconcileSpec(),
			"output":    Schema{"type": "array", "title": "Output", "items": Schema{"type": "string"}},
			"render":    Schema{"type": "string", "title": "Render", "enum": []string{"table", "logs"}, "description": "Presentation mode: table (default) or logs (canonical LogsTable view for trace/log profiles)"},
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
// profilePathDelimiters are the characters a profile name uses to encode its
// hierarchy — `jms.incoming` nests, `remote-debugger` does not. Kept in sync with
// cmd/query/profiles.profilePathDelimiters, which emits the same split as the
// sidebar's x-clicky-path.
// celEditorComponent marks a field whose value is a CEL expression, so the form
// offers an editor that knows the bindings and can evaluate against sample rows
// rather than a bare text input. The scope travels beside it in
// `x-clicky-cel-scope`, because the same expression text means different things
// under a column and under a processor's `set`.
const celEditorComponent = "cel-editor"

const profilePathDelimiters = "./"

// profileRefLookup is the x-clicky-lookup for a field naming another profile. It
// declares the naming convention's delimiters, so the picker browses the same
// hierarchy the sidebar shows instead of scrolling one flat list of every profile.
func profileRefLookup(multi bool) Schema {
	return Schema{
		"url":         "/api/v1/profiles",
		"filter":      "profile",
		"searchParam": "__lookup_q",
		"multi":       multi,
		"hierarchy":   Schema{"delimiters": profilePathDelimiters},
	}
}

// ambientConnectionHints name, per provider, the identity a query runs as when
// no connection is picked. Every one of these backends falls through to its
// platform's own credential chain, so leaving the field empty is a supported
// choice rather than an omission — the form has to say which identity that is.
var ambientConnectionHints = map[string]string{
	"cloudwatch":        "Leave empty to use the ambient AWS credentials (environment, IRSA, or instance profile)",
	"gcpcloudlogging":   "Leave empty to use the ambient Google application default credentials",
	"bigquery":          "Leave empty to use the ambient Google application default credentials",
	"azureloganalytics": "Leave empty to use the ambient Azure identity (environment, workload or managed identity, or az login)",
	"k8s":               "Leave empty to use the ambient cluster ($KUBECONFIG, ~/.kube/config, or the in-cluster service account)",
}

func connectionProp(providerType string) Schema {
	description := "Pick a saved connection or type an inline DSN/URL"
	if hint, ok := ambientConnectionHints[providerType]; ok {
		description = "Pick a saved connection. " + hint
	}

	return Schema{
		"type":        "string",
		"title":       "Connection",
		"description": description,
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
