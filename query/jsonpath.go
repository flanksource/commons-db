package query

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ohler55/ojg/jp"
)

// compileColumnJSONPath parses a column's path once. CEL leans on gomplate's
// program cache to survive being handed the same expression per row; ojg has no
// such cache, so the parse is hoisted out of the row loop and a malformed path
// fails when the profile is validated rather than on the first row that reaches
// it.
func compileColumnJSONPath(column ColumnDef) (jp.Expr, error) {
	expression, err := jp.ParseString(column.JSONPath)
	if err != nil {
		return nil, fmt.Errorf("column %q jsonpath %q is invalid: %w", column.Name, column.JSONPath, err)
	}
	return expression, nil
}

// evalRowJSONPath resolves a compiled path against a row, or against the row's
// source key when one is named.
//
// A single match is the value; several are the list of them. No match is nil —
// the honest reading of a field this row does not carry, and the one that lets a
// scan continue past rows with a sparser shape than the profile's author had in
// front of them.
func evalRowJSONPath(expression jp.Expr, source string, row Row) (any, error) {
	root, err := jsonPathRoot(source, row)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, nil
	}
	results := expression.Get(root)
	switch len(results) {
	case 0:
		return nil, nil
	case 1:
		return results[0], nil
	default:
		return results, nil
	}
}

// EvalJSONPath resolves an ad-hoc path against a row for profile authoring
// tools, which need to show an author what their half-written path selects.
//
// Unlike evalRowJSONPath it hands back every match rather than collapsing 0/1/N
// into nil/value/slice: the collapse is what a column cell wants, and a preview
// that showed nil for both "no match" and "matched null" would hide the one
// mistake it exists to catch.
func EvalJSONPath(expression, source string, row Row) ([]any, error) {
	parsed, err := jp.ParseString(expression)
	if err != nil {
		return nil, fmt.Errorf("jsonpath %q is invalid: %w", expression, err)
	}
	root, err := jsonPathRoot(source, row)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, nil
	}
	return parsed.Get(root), nil
}

// FilterFieldForJSONPath names the backend field a jsonpath filters on, when the
// path is a plain chain of literal keys.
//
// Such a path addresses exactly one field of the document, so the dotted join of
// its segments is the name a document store indexes it under — which is what
// makes a promoted JSON column filterable without the author writing
// filter.field by hand. Anything that selects rather than addresses — a filter
// expression, a wildcard, a descent, an array index — matches a set whose size
// depends on the row, so it names no field and the column stays unfilterable
// unless the author declares one.
func FilterFieldForJSONPath(expression, source string) (string, bool) {
	parsed, err := jp.ParseString(expression)
	if err != nil {
		return "", false
	}
	segments := make([]string, 0, len(parsed)+1)
	if source != "" {
		segments = append(segments, source)
	}
	for index, fragment := range parsed {
		switch typed := fragment.(type) {
		case jp.Root:
			if index != 0 {
				return "", false
			}
		case jp.Child:
			segments = append(segments, string(typed))
		default:
			return "", false
		}
	}
	if len(segments) == 0 {
		return "", false
	}
	return strings.Join(segments, "."), true
}

// jsonPathRoot returns the value a path runs against. A source carrying JSON as
// text is decoded, so a provider that hands back an encoded column and one that
// hands back a decoded column are read by the same path — the distinction is the
// transport's, not the author's.
//
// gomplate's jsonpath() decodes only into an object, which is why the CEL form
// of this needs the caller to pick .JSON() or .JSONArray() by hand; decoding
// into any covers an array root as well.
func jsonPathRoot(source string, row Row) (any, error) {
	if source == "" {
		return map[string]any(row), nil
	}
	value, present := row[source]
	if !present || value == nil {
		return nil, nil
	}
	text, encoded := value.(string)
	if !encoded {
		return value, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return nil, fmt.Errorf("source %q holds a string that is not JSON: %w", source, err)
	}
	return decoded, nil
}
