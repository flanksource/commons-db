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
			mapped, _, err := d.resolve(name, direction)
			if err != nil {
				return nil, err
			}
			object["field"] = mapped
			return node, nil
		case "term", "terms", "range", "match", "match_phrase", "match_phrase_prefix", "prefix", "wildcard", "regexp", "fuzzy":
			return d.leaf(op, operand, direction)
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
