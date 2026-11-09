package layout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func (d Definition) Translate(body []byte, direction Direction) ([]byte, error) {
	if direction != ToSource && direction != ToCanonical {
		return nil, fmt.Errorf("invalid translation direction %q", direction)
	}
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("search must contain one JSON document")
	}
	if document == nil {
		return nil, fmt.Errorf("search must be an object")
	}
	for key, value := range document {
		var err error
		switch key {
		case "query":
			document[key], err = d.condition(value, direction)
		case "sort":
			document[key], err = d.sort(value, direction)
		case "_source":
			document[key], err = d.source(value, direction)
		case "fields", "stored_fields":
			document[key], err = d.fields(value, fieldSelection{Direction: direction})
		case "size", "from", "track_total_hits":
		default:
			return nil, fmt.Errorf("search property %q has no canonical translation", key)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
	}
	return json.Marshal(document)
}

func (d Definition) condition(value any, direction Direction) (any, error) {
	node, ok := value.(map[string]any)
	if !ok || len(node) != 1 {
		return nil, fmt.Errorf("query condition must contain exactly one operator")
	}
	for op, operand := range node {
		switch op {
		case "match_all", "match_none":
			if object, ok := operand.(map[string]any); !ok || len(object) > 0 {
				return nil, fmt.Errorf("%s must be empty", op)
			}
			return node, nil
		case "bool":
			return d.boolean(operand, direction)
		case "exists":
			object, ok := operand.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("exists must be an object")
			}
			name, ok := object["field"].(string)
			if !ok {
				return nil, fmt.Errorf("exists requires field")
			}
			if list, key, ok := d.nestedTarget(name, direction); ok {
				return nestedClause(list, key, nil), nil
			}
			mapped, _, err := d.resolve(name, direction)
			if err != nil {
				return nil, err
			}
			object["field"] = mapped
			return node, nil
		case "term", "terms", "range", "match", "match_phrase", "match_phrase_prefix", "prefix", "wildcard", "regexp", "fuzzy":
			return d.leaf(op, operand, direction)
		case "nested":
			if direction != ToCanonical {
				return nil, fmt.Errorf("operator %q has no reversible canonical translation", op)
			}
			return d.foldNested(operand)
		case "multi_match", "query_string", "simple_query_string":
			object, ok := operand.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s must be an object", op)
			}
			if op != "multi_match" && strings.Contains(fmt.Sprint(object["query"]), ":") {
				return nil, fmt.Errorf("embedded query-string field references cannot be translated safely")
			}
			fields, exists := object["fields"]
			if !exists {
				return nil, fmt.Errorf("%s requires explicit canonical fields", op)
			}
			mapped, err := d.fields(fields, fieldSelection{Direction: direction, Text: true})
			if err != nil {
				return nil, err
			}
			object["fields"] = mapped
			return node, nil
		default:
			return nil, fmt.Errorf("operator %q has no reversible canonical translation", op)
		}
	}
	return nil, fmt.Errorf("empty query condition")
}

func (d Definition) boolean(value any, direction Direction) (any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("bool must be an object")
	}
	for occur, children := range object {
		if occur == "minimum_should_match" || occur == "boost" {
			continue
		}
		if occur != "filter" && occur != "must" && occur != "must_not" && occur != "should" {
			return nil, fmt.Errorf("unsupported bool property %q", occur)
		}
		if list, ok := children.([]any); ok {
			for index, child := range list {
				mapped, err := d.condition(child, direction)
				if err != nil {
					return nil, err
				}
				list[index] = mapped
			}
		} else {
			mapped, err := d.condition(children, direction)
			if err != nil {
				return nil, err
			}
			object[occur] = mapped
		}
	}
	return map[string]any{"bool": object}, nil
}

func (d Definition) leaf(op string, operand any, direction Direction) (any, error) {
	object, ok := operand.(map[string]any)
	if !ok || len(object) != 1 {
		return nil, fmt.Errorf("%s requires exactly one field", op)
	}
	for name, value := range object {
		if list, key, ok := d.nestedTarget(name, direction); ok {
			if op == "range" {
				return nil, fmt.Errorf("range on %s is unsupported: nested %s values are keyword strings and would compare lexically; index a typed field (Jaeger tags-as-fields or an ingest pipeline) to filter it by range", name, list.Path)
			}
			return nestedClause(list, key, map[string]any{op: map[string]any{list.Value: value}}), nil
		}
		mapped, field, err := d.resolve(name, direction)
		if err != nil {
			return nil, err
		}
		canonical := name
		if direction == ToCanonical {
			canonical = mapped
		}
		if op != "term" && op != "terms" && op != "range" && (field.Unit != "" || len(field.Values) > 0) {
			return nil, fmt.Errorf("%s cannot preserve the canonical value semantics of %s", op, canonical)
		}
		if op == "range" {
			if err := field.validateOrdering(); err != nil {
				return nil, err
			}
		}
		converted, err := field.operand(op, value, canonical, direction)
		if err != nil {
			return nil, err
		}
		if canonical == "status" && (op == "term" || op == "terms") {
			containsUnset := func(value any) bool {
				if object, ok := value.(map[string]any); ok {
					value = object["value"]
				}
				if list, ok := value.([]any); ok {
					for _, item := range list {
						if fmt.Sprint(item) == "unset" {
							return true
						}
					}
					return false
				}
				return fmt.Sprint(value) == "unset"
			}
			logical := value
			if direction == ToCanonical {
				logical = converted
			}
			if containsUnset(logical) {
				condition := map[string]any{op: map[string]any{mapped: converted}}
				exists := map[string]any{"exists": map[string]any{"field": mapped}}
				if direction == ToSource {
					return map[string]any{"bool": map[string]any{"should": []any{condition, map[string]any{"bool": map[string]any{"must_not": []any{exists}}}}, "minimum_should_match": 1}}, nil
				}
				return map[string]any{"bool": map[string]any{"filter": []any{condition, exists}}}, nil
			}
		}
		if field.Type == "text" && (op == "match" || op == "match_phrase" || op == "match_phrase_prefix") && direction == ToSource {
			mapped = field.Source
		}
		return map[string]any{op: map[string]any{mapped: converted}}, nil
	}
	return nil, fmt.Errorf("empty %s", op)
}

func (f Field) operand(op string, value any, canonical string, direction Direction) (any, error) {
	if op == "terms" {
		list, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("terms lookup queries have no canonical translation")
		}
		for i, item := range list {
			mapped, err := f.value(item, canonical, direction)
			if err != nil {
				return nil, err
			}
			list[i] = mapped
		}
		return list, nil
	}
	if object, ok := value.(map[string]any); ok {
		for key, item := range object {
			switch key {
			case "gt", "gte", "lt", "lte", "value", "query":
				mapped, err := f.value(item, canonical, direction)
				if err != nil {
					return nil, err
				}
				object[key] = mapped
			case "format", "time_zone":
				if f.Unit != "" {
					return nil, fmt.Errorf("%s cannot apply date formatting to numeric source field %s", key, f.Source)
				}
			}
		}
		return object, nil
	}
	return f.value(value, canonical, direction)
}

type fieldSelection struct {
	Direction Direction
	Source    bool
	Text      bool
}

func (d Definition) fields(value any, selection fieldSelection) (any, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("fields must be an array")
	}
	for i, item := range list {
		name, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("field must be a string")
		}
		base, boost, hasBoost := strings.Cut(name, "^")
		mapped, field, err := d.resolve(base, selection.Direction)
		if err != nil {
			return nil, err
		}
		if selection.Text && (field.Unit != "" || len(field.Values) > 0) {
			return nil, fmt.Errorf("text conditions cannot preserve the canonical value semantics of %s", base)
		}
		if (selection.Source || selection.Text) && selection.Direction == ToSource && field.Source != "" {
			mapped = field.Source
		}
		if hasBoost {
			mapped += "^" + boost
		}
		list[i] = mapped
	}
	return list, nil
}

func (d Definition) source(value any, direction Direction) (any, error) {
	switch source := value.(type) {
	case bool:
		return source, nil
	case []any:
		return d.fields(source, fieldSelection{Direction: direction, Source: true})
	case map[string]any:
		for key, items := range source {
			if key != "includes" && key != "excludes" {
				return nil, fmt.Errorf("unsupported source selector %q", key)
			}
			mapped, err := d.fields(items, fieldSelection{Direction: direction, Source: true})
			if err != nil {
				return nil, err
			}
			source[key] = mapped
		}
		return source, nil
	default:
		return nil, fmt.Errorf("source must be a boolean, array or object")
	}
}

func (d Definition) sort(value any, direction Direction) (any, error) {
	list, ok := value.([]any)
	if !ok {
		list = []any{value}
	}
	for i, item := range list {
		if name, ok := item.(string); ok {
			if nestedList, key, ok := d.nestedTarget(name, direction); ok {
				list[i] = nestedSort(nestedList, key, map[string]any{})
				continue
			}
			mapped, field, err := d.resolve(name, direction)
			if err != nil {
				return nil, err
			}
			if err := field.validateOrdering(); err != nil {
				return nil, err
			}
			list[i] = mapped
			continue
		}
		object, ok := item.(map[string]any)
		if !ok || len(object) != 1 {
			return nil, fmt.Errorf("sort requires one field")
		}
		for name, options := range object {
			if nestedList, key, ok := d.nestedTarget(name, direction); ok {
				settings, ok := options.(map[string]any)
				if !ok {
					settings = map[string]any{"order": options}
				}
				list[i] = nestedSort(nestedList, key, settings)
				continue
			}
			if direction == ToCanonical {
				if canonical, settings, ok, err := d.foldNestedSort(name, options); err != nil {
					return nil, err
				} else if ok {
					list[i] = map[string]any{canonical: settings}
					continue
				}
			}
			mapped, field, err := d.resolve(name, direction)
			if err != nil {
				return nil, err
			}
			if err := field.validateOrdering(); err != nil {
				return nil, err
			}
			list[i] = map[string]any{mapped: options}
		}
	}
	return list, nil
}

// nestedTarget reports that canonical name, translated to source, is read from
// a nested key/value list rather than a scalar field.
func (d Definition) nestedTarget(name string, direction Direction) (NestedAttributes, string, bool) {
	if direction != ToSource {
		return NestedAttributes{}, "", false
	}
	if _, scalar := d.Fields[name]; scalar {
		return NestedAttributes{}, "", false
	}
	list, ok := d.nestedList(name)
	if !ok {
		return NestedAttributes{}, "", false
	}
	return list, strings.TrimPrefix(name, list.Prefix), true
}

// nestedClause selects the list entry keyed key, and with inner also requires
// that entry's value to match. Without inner it is the attribute's `exists`.
func nestedClause(list NestedAttributes, key string, inner map[string]any) map[string]any {
	filter := []any{map[string]any{"term": map[string]any{list.Key: key}}}
	if inner != nil {
		filter = append(filter, inner)
	}
	return map[string]any{"nested": map[string]any{"path": list.Path, "query": map[string]any{"bool": map[string]any{"filter": filter}}}}
}

func nestedSort(list NestedAttributes, key string, settings map[string]any) map[string]any {
	settings["nested"] = map[string]any{"path": list.Path, "filter": map[string]any{"term": map[string]any{list.Key: key}}}
	return map[string]any{list.Value: settings}
}

// foldNested restores the canonical condition nestedClause produced. Any other
// nested query has no canonical meaning and is rejected.
func (d Definition) foldNested(operand any) (any, error) {
	body, ok := operand.(map[string]any)
	if !ok || len(body) != 2 {
		return nil, fmt.Errorf("nested query has no reversible canonical translation")
	}
	path, _ := body["path"].(string)
	list, ok := d.nestedSource(path)
	if !ok {
		return nil, fmt.Errorf("nested path %q is not a canonical attribute list", path)
	}
	query, _ := body["query"].(map[string]any)
	boolean, _ := query["bool"].(map[string]any)
	filter, _ := boolean["filter"].([]any)
	if len(query) != 1 || len(boolean) != 1 || len(filter) < 1 || len(filter) > 2 {
		return nil, fmt.Errorf("nested %s query must pin one %s and at most one value condition", path, list.Key)
	}
	key, err := pinnedKey(filter[0], list.Key)
	if err != nil {
		return nil, err
	}
	canonical := list.Prefix + key
	if len(filter) == 1 {
		return map[string]any{"exists": map[string]any{"field": canonical}}, nil
	}
	leaf, ok := filter[1].(map[string]any)
	if !ok || len(leaf) != 1 {
		return nil, fmt.Errorf("nested %s value condition must contain one operator", path)
	}
	for op, operand := range leaf {
		fields, ok := operand.(map[string]any)
		value, has := fields[list.Value]
		if !ok || len(fields) != 1 || !has {
			return nil, fmt.Errorf("nested %s value condition must target %s", path, list.Value)
		}
		return map[string]any{op: map[string]any{canonical: value}}, nil
	}
	return nil, fmt.Errorf("empty nested value condition")
}

func (d Definition) foldNestedSort(name string, options any) (string, any, bool, error) {
	settings, ok := options.(map[string]any)
	if !ok {
		return "", nil, false, nil
	}
	nested, ok := settings["nested"].(map[string]any)
	if !ok {
		return "", nil, false, nil
	}
	path, _ := nested["path"].(string)
	list, ok := d.nestedSource(path)
	if !ok || list.Value != name || len(nested) != 2 {
		return "", nil, false, fmt.Errorf("nested sort on %s has no reversible canonical translation", name)
	}
	key, err := pinnedKey(nested["filter"], list.Key)
	if err != nil {
		return "", nil, false, err
	}
	restored := map[string]any{}
	for option, value := range settings {
		if option != "nested" {
			restored[option] = value
		}
	}
	return list.Prefix + key, restored, true, nil
}

// pinnedKey reads the attribute name from the `term` selecting a list entry.
func pinnedKey(clause any, keyField string) (string, error) {
	object, _ := clause.(map[string]any)
	term, _ := object["term"].(map[string]any)
	key, ok := term[keyField].(string)
	if !ok || len(term) != 1 || len(object) != 1 || key == "" {
		return "", fmt.Errorf("nested condition must pin the entry with a term on %s", keyField)
	}
	return key, nil
}
