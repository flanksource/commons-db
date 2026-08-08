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

// JSONPathFilterTarget is how a jsonpath column pushes a selection down to the
// backend: the field the picked values compare against, the container that field
// sits inside, and the constants that address one entry of that container.
type JSONPathFilterTarget struct {
	// Field is the backend field the selection applies to.
	Field string

	// Container names the field the path descends through to reach Field, when it
	// reaches it by picking one entry rather than by naming it. It is set only
	// alongside Where.
	//
	// Whether the backend can actually correlate Where with Field inside it is
	// not something the path can know — it depends on how the container is
	// mapped, which is why a profile declares it as filter.nested and the
	// connection browser reads it from the index mapping.
	Container string

	// Where is the constants the selection also requires, keyed by backend field.
	// It is what addresses one entry of a key/value tag list: the path fixes the
	// key and the operator picks the value.
	Where map[string]string
}

// FilterTargetForJSONPath resolves the backend selection a jsonpath filters
// through, for the two shapes that address a field rather than merely select
// values.
//
// A plain chain of literal keys addresses exactly one field of the document, so
// the dotted join of its segments is the name a document store indexes it
// under — which is what makes a promoted JSON column filterable without the
// author writing filter.field by hand.
//
// A chain that steps through one equality filter and carries on — the shape of
// `$.tags[?(@.key == 'app')].value` — addresses one field too, but only of the
// entry the equality picks. That is the shape tags arrive in from Jaeger and
// OpenTelemetry, and reading it here is what lets such a column be narrowed at
// the index instead of after the rows are home.
//
// Anything else — a wildcard, a descent, an array index, an inequality, a second
// filter — matches a set whose size depends on the row, so it names no field and
// the column stays unfilterable unless the author declares one.
func FilterTargetForJSONPath(expression, source string) (JSONPathFilterTarget, bool) {
	parsed, err := jp.ParseString(expression)
	if err != nil {
		return JSONPathFilterTarget{}, false
	}
	segments := make([]string, 0, len(parsed)+1)
	if source != "" {
		segments = append(segments, source)
	}
	var target JSONPathFilterTarget
	picked := -1
	for index, fragment := range parsed {
		switch typed := fragment.(type) {
		case jp.Root:
			if index != 0 {
				return JSONPathFilterTarget{}, false
			}
		case jp.Child:
			segments = append(segments, string(typed))
		case *jp.Filter:
			// A second filter would pick an entry of an entry, which no backend
			// field names; a first one with nothing ahead of it has no container.
			if picked >= 0 || len(segments) == 0 {
				return JSONPathFilterTarget{}, false
			}
			picked = len(segments)
			target.Container = strings.Join(segments, ".")
			where, addressed := filterConstants(typed, target.Container)
			if !addressed {
				return JSONPathFilterTarget{}, false
			}
			target.Where = where
		default:
			return JSONPathFilterTarget{}, false
		}
	}
	// A path ending at the filter selects whole entries, and an entry is not a
	// field: there is nothing for a value to be compared against.
	if len(segments) == 0 || picked == len(segments) {
		return JSONPathFilterTarget{}, false
	}
	target.Field = strings.Join(segments, ".")
	return target, true
}

// filterConstants reads the equalities a filter fragment pins, as backend field
// names under container. Only a conjunction of `@.<key> == "<literal>"` counts:
// every other operator selects a range or a set rather than addressing one
// entry, and a selection compiled from it would narrow to more entries than the
// column's own value came from.
func filterConstants(filter *jp.Filter, container string) (map[string]string, bool) {
	constants := map[string]string{}
	if !collectFilterConstants(filter.Inspect(), container, constants) || len(constants) == 0 {
		return nil, false
	}
	return constants, true
}

func collectFilterConstants(form *jp.Form, container string, into map[string]string) bool {
	if form == nil {
		return false
	}
	if form.Op == "&&" {
		left, isLeftForm := form.Left.(*jp.Form)
		right, isRightForm := form.Right.(*jp.Form)
		return isLeftForm && isRightForm &&
			collectFilterConstants(left, container, into) &&
			collectFilterConstants(right, container, into)
	}
	if form.Op != "==" {
		return false
	}
	name, value, ok := filterEquality(form)
	if !ok {
		return false
	}
	field := container + "." + name
	// The same key pinned to two values matches nothing, so it is refused rather
	// than compiled into a clause that can never be true.
	if existing, seen := into[field]; seen && existing != value {
		return false
	}
	into[field] = value
	return true
}

// filterEquality reads one `@.<key> == "<literal>"`, either way round.
func filterEquality(form *jp.Form) (name, value string, ok bool) {
	if name, ok = filterRelativeField(form.Left); ok {
		value, ok = form.Right.(string)
		return name, value, ok
	}
	if name, ok = filterRelativeField(form.Right); ok {
		value, ok = form.Left.(string)
		return name, value, ok
	}
	return "", "", false
}

// filterRelativeField reads `@.<key>` — one key of the entry under test. A
// deeper path names a field of a field, which the flattening a document store
// applies to a tag list does not preserve.
func filterRelativeField(side any) (string, bool) {
	expression, ok := side.(jp.Expr)
	if !ok || len(expression) != 2 {
		return "", false
	}
	if _, isAt := expression[0].(jp.At); !isAt {
		return "", false
	}
	child, isChild := expression[1].(jp.Child)
	return string(child), isChild
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
