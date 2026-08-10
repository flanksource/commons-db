package schema

import (
	"strings"

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
		"date": "calendar", "enum": "tag", "list": "list-dashes",
	}
	paramTypeTones = map[string]string{
		"string": "slate", "number": "violet", "boolean": "amber",
		"date": "sky", "enum": "teal", "list": "indigo",
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
	"opensearch":    {models.ConnectionTypeOpenSearch},
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
				"enum":           []string{"string", "number", "boolean", "date", "enum", "list"},
				"x-enum-labels":  map[string]string{"list": "List (multi-select)"},
				"x-enum-icons":   paramTypeIcons,
				"x-enum-tones":   paramTypeTones,
				"x-enum-display": "combobox",
				"description":    "A list accepts several values at once; bind it to a field to allow excluding them",
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
				"description":        "Backend field this parameter filters on; enables excluding a value and requires a provider that applies native filters (OpenSearch, OpenTelemetry, or SQL)",
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
			"cel":   Schema{"type": "string", "title": "CEL", "description": "Expression computing the cell value from `row`", "x-clicky-order": 8},
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
							"terms":   "Value selection",
							"range":   "Numeric range",
							"time":    "Time range",
							"boolean": "Yes/no",
							"text":    "Substring",
							"none":    "Not filterable",
						},
						"x-enum-display": "combobox",
						"description":    "Overrides the control derived from Type; set it where the rendered type and the backend storage disagree",
					},
					"options": Schema{
						"type": "array", "items": Schema{"type": "string"}, "title": "Options", "x-col-span": "full",
						"description": "Selectable values; leave empty to ask the backend for its own distinct values",
					},
					"lookup": Schema{
						"type": "boolean", "title": "Lookup", "default": true,
						"description": "Ask the backend for this field's distinct values; turn off for a high-cardinality field that is typed rather than picked",
					},
					"limit": Schema{
						"type": "integer", "title": "Limit", "default": query.DefaultFilterLookupLimit,
						"minimum": 1, "maximum": query.MaxFilterLookupLimit,
						"description": "How many distinct values the control offers before the rest have to be typed for; requires Lookup",
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
			"hidden": Schema{"type": "boolean", "title": "Hidden", "description": "Hide the column from default output while retaining it for CEL and processors", "x-clicky-order": 11},
		},
	}
	aliasDef := Schema{
		"type": "object", "required": []string{"name", "cel"},
		"properties": Schema{"name": strProp("Name", "Dotted output path"), "cel": strProp("CEL", "Ordered row projection")},
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
			"order": Schema{
				"type": "array", "title": "Order", "x-layout": "table", "items": orderBy,
				"description": "Total order the rows are returned in, ending in a column marked unique. Paging past the first page requires it: without a tiebreaker, two runs of the same query may interleave tied rows differently and a second page can repeat or skip rows from the first.",
			},
			"processors": Schema{
				"type": "array", "title": "Processors", "x-layout": "table", "items": processorSpec(),
				"description": "Post-query steps applied in order, after columns and aliases",
			},
			"replay":    replaySpec(),
			"reconcile": reconcileSpec(),
			"output":    Schema{"type": "array", "title": "Output", "items": Schema{"type": "string"}},
			"render":    Schema{"type": "string", "title": "Render", "enum": []string{"table", "logs"}, "description": "Presentation mode: table (default) or logs (canonical LogsTable view for trace/log profiles)"},
		},
	}
}

// processorSpec describes one post-query step. Both pickers are driven by the
// live registries, so a processor or library preset that is registered but not
// listed here cannot happen. The enums are omitted rather than emitted empty
// when nothing is registered — an empty JSON Schema enum admits no value at
// all, which would render as a dead control.
func processorSpec() Schema {
	use := Schema{
		"type": "string", "title": "Library processor", "x-clicky-order": 0,
		"description": "Reusable preset supplying the type and its configuration; anything set below is merged over it",
	}
	if names := query.NamedProcessorNames(); len(names) > 0 {
		use["enum"] = names
		use["x-enum-labels"] = namedProcessorLabels()
		use["x-enum-display"] = "combobox"
		use["description"] = use["description"].(string) + ".\n\n" + namedProcessorHelp()
	}

	typ := Schema{
		"type": "string", "title": "Type", "x-clicky-order": 1,
		"description": "Registered processor key; leave blank when a library processor is chosen",
	}
	if types := query.RegisteredProcessors(); len(types) > 0 {
		typ["enum"] = types
	}

	return Schema{
		"type":  "object",
		"title": "Processor",
		"properties": Schema{
			"use":    use,
			"type":   typ,
			"config": Schema{"type": "object", "title": "Config", "x-clicky-order": 2, "description": "Processor-specific configuration"},
		},
	}
}

// namedProcessorLabels maps each library key to its short title, which is what
// the picker lists.
func namedProcessorLabels() map[string]string {
	labels := map[string]string{}
	for _, entry := range query.NamedProcessors() {
		if entry.Title != "" {
			labels[entry.Name] = entry.Title
		}
	}
	return labels
}

// namedProcessorHelp spells out what each preset does, so the author choosing
// one does not have to open its source to find out.
func namedProcessorHelp() string {
	var help []string
	for _, entry := range query.NamedProcessors() {
		if entry.Description != "" {
			help = append(help, "- `"+entry.Name+"`: "+entry.Description)
		}
	}
	return strings.Join(help, "\n")
}

// replaySpec describes the profile's replay block. Every field except target
// and kind is a CEL expression over the result row, so the form labels them as
// such rather than as literal values.
func replaySpec() Schema {
	return Schema{
		"type":        "object",
		"title":       "Replay",
		"description": "Turn one result row back into an outbound HTTP request",
		"properties": Schema{
			"kind": Schema{
				"type": "string", "title": "Kind", "enum": []string{"http"}, "default": "http",
				"x-clicky-order": 1,
			},
			"target": Schema{
				"type":               "object",
				"title":              "Target",
				"description":        "Connection the request is sent to; required for a relative URL",
				"properties":         Schema{"connection": connectionProp(""), "url": strProp("URL", "Base URL")},
				"x-clicky-order":     2,
				"x-clicky-component": "connection-http",
			},
			"method": strProp("Method", `CEL expression yielding the HTTP method, e.g. "POST" (defaults to POST)`),
			"url":    strProp("URL", "CEL expression yielding an absolute URL or a path relative to the target"),
			"body": Schema{
				"type": "string", "title": "Body", "format": "textarea",
				"description": "CEL expression yielding the request body; non-string values are JSON encoded",
			},
			"headers": Schema{
				"type":                 "object",
				"title":                "Headers",
				"description":          "Header name to CEL expression; an expression yielding blank omits the header",
				"additionalProperties": Schema{"type": "string"},
			},
		},
	}
}

// reconcileSpec describes the profile's reconcile block: the profile this one is
// habitually joined against, how the shared identity is derived, and the row
// bound a check runs under. Columns and CEL are alternatives, not a pair — the
// engine rejects a key that sets both — so the description says so where the
// form cannot enforce it.
func reconcileSpec() Schema {
	return Schema{
		"type":        "object",
		"title":       "Reconcile",
		"description": "Join this profile's rows against another profile on a shared identity",
		"properties": Schema{
			"dest": Schema{
				"type": "string", "title": "Destination profile",
				"description":     "The profile to reconcile against",
				"x-clicky-order":  1,
				"x-clicky-lookup": profileRefLookup(false),
			},
			"key": Schema{
				"type":           "object",
				"title":          "Key",
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
			"timeColumn": strProp("Time column",
				"Row key holding each side's event time; defaults to the profile's timestamp column"),
			"range": Schema{
				"type": "object", "title": "Key range",
				"description": "Span of keys to reconcile; empty covers all of them. A range cuts both sides at the same keys, so a key missing from one side inside it is missing rather than merely unread",
				"properties": Schema{
					"from": strProp("From", "Reconcile keys at or after this one; empty starts at the first key"),
					"to":   strProp("To", "Reconcile keys before this one; empty runs to the last key"),
				},
			},
			"params": Schema{
				"type":                 "object",
				"title":                "Params",
				"description":          "Filter values applied to whichever side declares each one",
				"additionalProperties": Schema{"type": "string"},
			},
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

// ProfileInstance returns a per-profile schema: the top-level `properties`
// describe the FilterBar inputs (from the profile's Params) and `x-clicky-columns`
// describes the DataTable (from the profile's Columns).
func ProfileInstance(p query.Profile) (Schema, error) {
	props := Schema{}
	var required []string
	for _, def := range p.Params {
		props[def.Name] = paramSchema(def)
		if def.Required {
			required = append(required, def.Name)
		}
	}

	// The bindings are what the browser needs to render a filter and what the
	// request must name to apply one, so a profile whose filters cannot be
	// resolved describes no columns rather than columns with no filters.
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
		col := Schema{
			"name":  c.Name,
			"label": labelOr(c.Label, c.Name),
		}
		if binding, ok := filterByColumn[c.Name]; ok {
			filter := Schema{
				"key":    binding.Key,
				"kind":   string(binding.Kind),
				"multi":  binding.Multi,
				"lookup": binding.Lookup,
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
		"$schema":          Draft,
		"title":            p.Name,
		"type":             "object",
		"properties":       props,
		"x-clicky-columns": columns,
	}
	if render := p.RenderMode(); render != "" {
		s["x-clicky-render"] = render
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s, nil
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
	case query.ParamTypeList:
		s["type"] = "array"
		s["items"] = Schema{"type": "string"}
	default:
		s["type"] = "string"
	}
	if len(def.Options) > 0 {
		// A list constrains its elements, not the selection as a whole.
		if def.Type == query.ParamTypeList {
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
