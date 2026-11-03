package esdsl

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Parse imports supported OpenSearch DSL without discarding unknown clauses.
func Parse(body []byte) (Search, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		return Search{}, err
	}
	if document == nil {
		return Search{}, fmt.Errorf("search must be an object")
	}
	search := Search{}
	for name, raw := range document {
		var err error
		switch name {
		case "query":
			search.Query, err = parseCondition(raw)
		case "sort":
			search.Sort, err = parseSort(raw)
		case "_source":
			search.Source = &Source{}
			err = json.Unmarshal(raw, search.Source)
		case "size":
			search.Size = new(int)
			err = json.Unmarshal(raw, search.Size)
		case "from":
			search.From = new(int)
			err = json.Unmarshal(raw, search.From)
		case "fields":
			err = json.Unmarshal(raw, &search.Fields)
		case "stored_fields":
			err = json.Unmarshal(raw, &search.StoredFields)
		case "track_total_hits":
			search.TrackTotalHits = &TrackTotalHits{}
			err = json.Unmarshal(raw, search.TrackTotalHits)
		default:
			return Search{}, fmt.Errorf("search property %q has no structured representation", name)
		}
		if err != nil {
			return Search{}, fmt.Errorf("%s: %w", name, err)
		}
	}
	return search, search.Validate()
}

func parseCondition(raw []byte) (*Condition, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if len(object) != 1 {
		return nil, fmt.Errorf("condition requires exactly one operator")
	}
	for operator, operand := range object {
		c := &Condition{Op: Operator(operator)}
		switch c.Op {
		case OpMatchAll:
			var empty map[string]any
			if err := json.Unmarshal(operand, &empty); err != nil || len(empty) > 0 {
				return nil, fmt.Errorf("%s must be empty", operator)
			}
		case OpBool:
			var groups map[string]json.RawMessage
			if err := json.Unmarshal(operand, &groups); err != nil {
				return nil, err
			}
			for _, occur := range Occurs() {
				if children, ok := groups[string(occur)]; ok {
					var list []json.RawMessage
					if err := json.Unmarshal(children, &list); err != nil {
						list = []json.RawMessage{children}
					}
					for _, child := range list {
						parsed, err := parseCondition(child)
						if err != nil {
							return nil, err
						}
						parsed.Occur = occur
						c.Conditions = append(c.Conditions, *parsed)
					}
					delete(groups, string(occur))
				}
			}
			if value, ok := groups["minimum_should_match"]; ok {
				var decoded any
				_ = json.Unmarshal(value, &decoded)
				c.MinimumShouldMatch = fmt.Sprint(decoded)
				delete(groups, "minimum_should_match")
			}
			if len(groups) > 0 {
				return nil, fmt.Errorf("unsupported bool properties: %v", groups)
			}
		case OpExists:
			var fields map[string]string
			if err := json.Unmarshal(operand, &fields); err != nil {
				return nil, err
			}
			if len(fields) != 1 || fields["field"] == "" {
				return nil, fmt.Errorf("exists requires field")
			}
			c.Field = fields["field"]
		case OpMultiMatch, OpQueryString, OpSimpleQueryString:
			var qualifiers map[string]json.RawMessage
			if err := json.Unmarshal(operand, &qualifiers); err != nil {
				return nil, err
			}
			if err := parseQualifiers(c, qualifiers); err != nil {
				return nil, err
			}
		default:
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(operand, &fields); err != nil {
				return nil, err
			}
			if len(fields) != 1 {
				return nil, fmt.Errorf("%s requires exactly one field", operator)
			}
			for name, value := range fields {
				c.Field = name
				if c.Op == OpTerms {
					var list []json.RawMessage
					if err := json.Unmarshal(value, &list); err != nil {
						return nil, err
					}
					for _, item := range list {
						parsed, err := parseValue(item)
						if err != nil {
							return nil, err
						}
						c.Values = append(c.Values, *parsed)
					}
				} else {
					var qualifiers map[string]json.RawMessage
					if json.Unmarshal(value, &qualifiers) == nil && qualifiers != nil {
						if err := parseQualifiers(c, qualifiers); err != nil {
							return nil, err
						}
					} else {
						parsed, err := parseValue(value)
						if err != nil {
							return nil, err
						}
						c.Value = parsed
					}
				}
			}
		}
		return c, nil
	}
	return nil, fmt.Errorf("empty condition")
}

func parseValue(raw []byte) (*Value, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return Literal(value), nil
}

func parseQualifiers(c *Condition, fields map[string]json.RawMessage) error {
	for name, raw := range fields {
		var target any
		switch name {
		case "query", "value":
			value, err := parseValue(raw)
			if err != nil {
				return err
			}
			c.Value = value
			continue
		case "gt", "gte", "lt", "lte":
			value, err := parseValue(raw)
			if err != nil {
				return err
			}
			switch name {
			case "gt":
				c.Gt = value
			case "gte":
				c.Gte = value
			case "lt":
				c.Lt = value
			case "lte":
				c.Lte = value
			}
			continue
		case "fields":
			target = &c.Fields
		case "format":
			target = &c.Format
		case "time_zone":
			target = &c.TimeZone
		case "analyzer":
			target = &c.Analyzer
		case "operator":
			target = &c.MatchOperator
		case "type":
			target = &c.MultiMatchType
		case "fuzziness":
			target = &c.Fuzziness
		case "slop":
			target = &c.Slop
		case "boost":
			target = &c.Boost
		case "case_insensitive":
			target = &c.CaseInsensitive
		default:
			return fmt.Errorf("unsupported %s property %q", c.Op, name)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return err
		}
	}
	return nil
}

func parseSort(raw []byte) ([]SortBy, error) {
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		list = []json.RawMessage{raw}
	}
	result := []SortBy{}
	for _, item := range list {
		var name string
		if json.Unmarshal(item, &name) == nil {
			result = append(result, SortBy{Field: name})
			continue
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(item, &object); err != nil {
			return nil, err
		}
		if len(object) != 1 {
			return nil, fmt.Errorf("sort requires one field")
		}
		for field, options := range object {
			sort := SortBy{Field: field}
			var direction string
			if json.Unmarshal(options, &direction) == nil {
				sort.Order = direction
			} else {
				var properties map[string]json.RawMessage
				if err := json.Unmarshal(options, &properties); err != nil {
					return nil, err
				}
				for key, value := range properties {
					var target *string
					switch key {
					case "order":
						target = &sort.Order
					case "mode":
						target = &sort.Mode
					case "missing":
						target = &sort.Missing
					case "unmapped_type":
						target = &sort.UnmappedType
					default:
						return nil, fmt.Errorf("unsupported sort property %q", key)
					}
					if err := json.Unmarshal(value, target); err != nil {
						return nil, err
					}
				}
			}
			result = append(result, sort)
		}
	}
	return result, nil
}
