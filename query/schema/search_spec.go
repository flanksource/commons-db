package schema

import (
	"encoding/json"

	"github.com/flanksource/commons-db/query/esdsl"
)

// searchSpecDepth is how many levels of the condition tree the emitted schema
// spells out. JSON Schema can express this recursion with an internal $ref, but
// Bundle rewrites only external refs and overwrites root["$defs"], so a
// component's own $defs would dangle. Three levels covers hand-authored YAML
// completion; the interactive editor is delegated via x-clicky-component and is
// not depth-limited.
const searchSpecDepth = 3

// searchSpecProp is the structured search specification stored at
// provider.options.search. It is the source of truth for an OpenSearch query;
// the DSL is derived from it, which is what keeps parameters bound structurally
// instead of interpolated into a query string.
func searchSpecProp() Schema {
	return Schema{
		"type":  "object",
		"title": "Search",
		"description": "Structured OpenSearch query. Mutually exclusive with the profile query: " +
			"set one or the other, never both.",
		"x-clicky-component": "es-query-builder",
		"x-es-operators":     operatorCatalog(),
		"x-es-occurs":        occurNames(),
		"x-es-qualifiers":    qualifierOperators(),
		"properties": Schema{
			"query":     conditionSchema(searchSpecDepth),
			"timeField": strProp("Time field", "Date field that time-from and time-to params bind to"),
			"sort": Schema{
				"type": "array", "title": "Sort", "x-layout": "table",
				"items": Schema{
					"type":     "object",
					"required": []string{"field"},
					"properties": Schema{
						"field": strProp("Field", "Field name, or _score / _doc"),
						"order": Schema{"type": "string", "title": "Order", "enum": esdsl.SortOrders()},
						"mode":  strProp("Mode", "How a multi-valued field is reduced: min, max, sum, avg, median"),
						"missing": strProp("Missing",
							"Where documents without the field sort: _first, _last, or a substitute value"),
						"unmappedType": strProp("Unmapped type", "Treat the field as this type where it is unmapped"),
					},
				},
			},
			"size": Schema{"type": "integer", "title": "Size", "minimum": 0,
				"description": "Maximum hits returned. A limit-role param overrides it."},
			"from": Schema{"type": "integer", "title": "From", "minimum": 0,
				"description": "Hits to skip. Not available while scrolling."},
			"source": Schema{
				"type": "object", "title": "Source",
				"description": "Which _source fields come back",
				"properties": Schema{
					"enabled":  Schema{"type": "boolean", "title": "Enabled", "description": "Set false to omit _source"},
					"includes": Schema{"type": "array", "title": "Includes", "items": Schema{"type": "string"}},
					"excludes": Schema{"type": "array", "title": "Excludes", "items": Schema{"type": "string"}},
				},
			},
			"trackTotalHits": Schema{
				"type": "object", "title": "Track total hits",
				"description": "Whether the backend counts matches past its 10,000 default",
				"properties": Schema{
					"enabled":   Schema{"type": "boolean", "title": "Enabled"},
					"threshold": Schema{"type": "integer", "title": "Threshold", "minimum": 0},
				},
			},
			"storedFields": Schema{"type": "array", "title": "Stored fields", "items": Schema{"type": "string"}},
			"fields":       Schema{"type": "array", "title": "Fields", "items": Schema{"type": "string"}},
			"aggregations": Schema{"type": "object", "title": "Aggregations",
				"description": "Preserved verbatim; the query builder does not edit them"},
		},
	}
}

// conditionSchema is one node of the query tree. The innermost level omits
// `conditions`, which is what bounds the expansion.
func conditionSchema(depth int) Schema {
	props := Schema{
		"op":     Schema{"type": "string", "title": "Operator", "enum": operatorNames()},
		"occur":  Schema{"type": "string", "title": "Clause", "enum": occurNames(), "default": string(esdsl.OccurFilter)},
		"field":  strProp("Field", "Target field"),
		"fields": Schema{"type": "array", "title": "Fields", "items": Schema{"type": "string"}},
		"value": valueSchema("Value",
			"Operand: a literal, or {\"param\": \"<name>\"} to bind a profile param (escaped, and pruned when empty). "+
				"A literal string may also interpolate one as {{.params.<name>}}, which substitutes verbatim and fails when the param has no value"),
		"values": Schema{"type": "array", "title": "Values",
			"items": valueSchema("", "")},
		"gt":  valueSchema("Greater than", "Exclusive lower bound; a date field accepts date math"),
		"gte": valueSchema("Greater or equal", "Inclusive lower bound; a date field accepts date math"),
		"lt":  valueSchema("Less than", "Exclusive upper bound; a date field accepts date math"),
		"lte": valueSchema("Less or equal", "Inclusive upper bound; a date field accepts date math"),
		"optional": Schema{"type": "boolean", "title": "Optional",
			"description": "Drop this condition when its param resolves to nothing, instead of failing"},
		"when": strProp("When", "Emit this condition only while the named param has a value"),
	}
	for name, prop := range conditionQualifiers() {
		props[name] = prop
	}
	if depth > 1 {
		props["conditions"] = Schema{
			"type": "array", "title": "Conditions",
			"items": conditionSchema(depth - 1),
		}
	}
	return Schema{
		"type": "object", "title": "Condition", "required": []string{"op"}, "properties": props,
	}
}

// conditionQualifiers are the advanced per-operator settings. Which operators
// accept which qualifier is carried once, as x-es-qualifiers on the search
// specification — the same table the compiler validates against.
func conditionQualifiers() Schema {
	return Schema{
		"analyzer":        strProp("Analyzer", "Override the search analyzer"),
		"format":          strProp("Format", "Date format for a range over a date field"),
		"timeZone":        strProp("Time zone", "Time zone for a range over a date field"),
		"fuzziness":       strProp("Fuzziness", "Edit distance, or AUTO"),
		"slop":            Schema{"type": "integer", "title": "Slop", "minimum": 0},
		"boost":           Schema{"type": "number", "title": "Boost", "minimum": 0},
		"caseInsensitive": Schema{"type": "boolean", "title": "Case insensitive"},
		// No `default` here, nor on any other qualifier: a schema-driven form
		// materialises defaults onto every condition it edits, and escape is only
		// legal on the two query-string operators.
		"escape": Schema{"type": "boolean", "title": "Escape",
			"description": "Lucene-escape a param-sourced query string; on unless set. Turning it off trusts the param's author."},
		"matchOperator": Schema{"type": "string", "title": "Match operator", "enum": esdsl.MatchOperators()},
		"multiMatchType": Schema{"type": "string", "title": "Multi-match type",
			"enum": esdsl.MultiMatchTypes()},
		"scoreMode":          Schema{"type": "string", "title": "Score mode", "enum": esdsl.ScoreModes()},
		"path":               strProp("Path", "Nested object path"),
		"minimumShouldMatch": strProp("Minimum should match", "How many should clauses a hit must satisfy"),
	}
}

// valueSchema describes an operand. It carries no `type`: an operand is a
// literal of any JSON type, or a {"param": …} reference.
func valueSchema(title, description string) Schema {
	s := Schema{"x-es-value": true}
	if title != "" {
		s["title"] = title
	}
	if description != "" {
		s["description"] = description
	}
	return s
}

// operatorCatalog renders esdsl.Catalog as plain schema maps. Going through
// JSON keeps the emitted keys identical to the ones the frontend reads off the
// wire, so the operator vocabulary has exactly one definition.
func operatorCatalog() []any {
	data, err := json.Marshal(esdsl.Catalog())
	if err != nil {
		panic("marshal operator catalog: " + err.Error())
	}
	var entries []any
	if err := json.Unmarshal(data, &entries); err != nil {
		panic("decode operator catalog: " + err.Error())
	}
	return entries
}

func operatorNames() []string {
	names := make([]string, 0)
	for _, op := range esdsl.Operators() {
		names = append(names, string(op))
	}
	return names
}

func occurNames() []string {
	names := make([]string, 0)
	for _, occur := range esdsl.Occurs() {
		names = append(names, string(occur))
	}
	return names
}

func qualifierOperators() map[string][]string {
	qualifiers := esdsl.Qualifiers()
	out := make(map[string][]string, len(qualifiers))
	for _, name := range esdsl.QualifierNames() {
		ops := make([]string, 0, len(qualifiers[name]))
		for _, op := range qualifiers[name] {
			ops = append(ops, string(op))
		}
		out[name] = ops
	}
	return out
}
