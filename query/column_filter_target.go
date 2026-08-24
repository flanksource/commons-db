package query

import (
	"fmt"
	"slices"
	"strings"
)

// columnFilterTarget infers the backend selection a column filters through, and
// reports whether the profile declared it outright. A declared target is taken
// as written; an inferred one still passes through the provider's own naming.
//
// A column whose value is computed resolves to nothing and is silently left
// unfilterable: the value exists only after the row was read, so there is no
// backend field to push the selection down to, and an author who wants one says
// so with filter.field. That covers a CEL expression which is not a plain row
// lookup, and a JSONPath which selects rather than addresses — see
// FilterTargetForJSONPath, which reaches the literal-key-chain paths and the
// key-equality paths a tag list is read with.
func columnFilterTarget(column ColumnDef) (target JSONPathFilterTarget, declared bool, ok bool, err error) {
	if column.Filter != nil {
		if field := strings.TrimSpace(column.Filter.Field); field != "" {
			return JSONPathFilterTarget{Field: field}, true, true, nil
		}
	}
	if column.JSONPath != "" {
		if target, ok := FilterTargetForJSONPath(column.JSONPath, column.Source); ok {
			return target, false, true, nil
		}
		return JSONPathFilterTarget{}, false, false, nil
	}
	if column.Source != "" {
		return JSONPathFilterTarget{Field: column.Source}, false, true, nil
	}
	expression := strings.TrimSpace(column.CEL)
	if expression == "" {
		if column.Name == "" {
			return JSONPathFilterTarget{}, false, false, fmt.Errorf("column filter requires a name or an explicit filter field")
		}
		return JSONPathFilterTarget{Field: column.Name}, false, true, nil
	}
	if matches := directCELField.FindStringSubmatch(expression); len(matches) == 2 {
		return JSONPathFilterTarget{Field: matches[1]}, false, true, nil
	}
	if matches := indexedCELField.FindStringSubmatch(expression); len(matches) == 2 {
		return JSONPathFilterTarget{Field: matches[1]}, false, true, nil
	}
	return JSONPathFilterTarget{}, false, false, nil
}

// resolveColumnNesting settles which container a column's selection is compiled
// inside, and the constants that address one entry of it.
//
// The container is the author's to declare. A path that steps through a key
// equality knows an entry is being picked, but not whether the index maps that
// container as `nested` — and only inside a nested mapping do the key and the
// value have to come from the same entry. So a picked entry with no declared
// `nested` yields no filter at all, rather than a pair of clauses that would be
// ANDed across the whole document and match a document carrying the key on one
// entry and the value on another.
func resolveColumnNesting(column ColumnDef, target JSONPathFilterTarget) (nested string, where map[string]string, ok bool, err error) {
	if column.Filter != nil {
		nested = strings.TrimSpace(column.Filter.Nested)
		where = column.Filter.Where
	}
	if len(target.Where) == 0 {
		return nested, where, true, nil
	}
	if nested == "" {
		return "", nil, false, nil
	}
	if nested != target.Container {
		return "", nil, false, fmt.Errorf(
			"column %q filter declares nested %q but its jsonpath picks an entry of %q",
			column.Name, nested, target.Container)
	}
	if len(where) > 0 {
		// The path already says which entry it reads. A second answer beside it is
		// two selections wearing one name, and only one of them could be applied.
		return "", nil, false, fmt.Errorf(
			"column %q filter sets where, but its jsonpath already pins %s",
			column.Name, strings.Join(sortedFieldNames(target.Where), ", "))
	}
	return nested, target.Where, true, nil
}

// validateNestedField rejects a resolved field that its declared container does
// not hold. An inferred field is checked here rather than in ColumnFilterDef
// because it is not known until the column has been read.
func validateNestedField(column, field, nested string) error {
	if nested == "" || underNested(field, nested) {
		return nil
	}
	return fmt.Errorf("column %q filter field %q is not inside nested %q", column, field, nested)
}

// validateNestedProvider refuses a container on a backend with no notion of one.
// A SQL result is rows of columns; there is no entry inside a row for a
// selection to be scoped to, so a declaration of one is a mistake to report
// rather than a hint to drop.
func validateNestedProvider(providerType, owner, nested string) error {
	if nested == "" {
		return nil
	}
	if SupportsNestedFilters(providerType) {
		return nil
	}
	return fmt.Errorf("%s declares nested %q, which provider %q has no equivalent of", owner, nested, providerType)
}

// SupportsNestedFilters reports whether a provider type can scope a selection to
// one entry of a repeated field. It is the document stores: `nested` is their
// mapping, and their query language is the only one here with a clause for it.
func SupportsNestedFilters(providerType string) bool {
	switch providerType {
	case "opensearch", "opentelemetry":
		return true
	default:
		return false
	}
}

func sortedFieldNames(where map[string]string) []string {
	names := make([]string, 0, len(where))
	for name := range where {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func openTelemetryFilterField(profile Profile, inferred string) string {
	if profile.Provider.Type != "opentelemetry" {
		return inferred
	}
	defaults := map[string]string{
		"timestamp":      "@timestamp",
		"trace_id":       "trace_id",
		"span_id":        "span_id",
		"parent_id":      "parent_id",
		"service":        "service_name",
		"service_name":   "service_name",
		"operation":      "operation_name",
		"operation_name": "operation_name",
	}
	optionNames := map[string]string{
		"timestamp":      "dateField",
		"trace_id":       "traceIdField",
		"span_id":        "spanIdField",
		"parent_id":      "parentIdField",
		"service":        "serviceField",
		"service_name":   "serviceField",
		"operation":      "operationField",
		"operation_name": "operationField",
	}
	optionName, known := optionNames[inferred]
	if !known {
		return inferred
	}
	if configured, ok := profile.Provider.Options[optionName].(string); ok && strings.TrimSpace(configured) != "" {
		return configured
	}
	return defaults[inferred]
}
