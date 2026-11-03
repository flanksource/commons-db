package layout

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/commons-db/query/esdsl"
)

func (d Definition) TranslateSearch(search esdsl.Search, direction Direction) (esdsl.Search, error) {
	encoded, err := json.Marshal(search)
	if err != nil {
		return esdsl.Search{}, err
	}
	var result esdsl.Search
	if err := json.Unmarshal(encoded, &result); err != nil {
		return result, err
	}
	if result.Query != nil {
		if err := d.searchCondition(result.Query, direction); err != nil {
			return result, err
		}
	}
	for i, sort := range result.Sort {
		mapped, field, err := d.resolve(sort.Field, direction)
		if err != nil {
			return result, err
		}
		if err := field.validateOrdering(); err != nil {
			return result, err
		}
		result.Sort[i].Field = mapped
	}
	if result.TimeField != "" {
		mapped, field, err := d.resolve(result.TimeField, direction)
		if err != nil {
			return result, err
		}
		result.TimeField = mapped
		if direction == ToCanonical {
			result.TimeFieldFormat = ""
		} else {
			result.TimeFieldFormat = map[string]esdsl.TimeFieldFormat{"s": esdsl.TimeFieldFormatEpochSecond, "ms": esdsl.TimeFieldFormatEpochMillis, "us": esdsl.TimeFieldFormatEpochMicros, "ns": esdsl.TimeFieldFormatEpochNanos}[field.Unit]
		}
	}
	if result.Source != nil && (len(result.Source.Includes) > 0 || len(result.Source.Excludes) > 0) || len(result.Fields) > 0 || len(result.StoredFields) > 0 || len(result.Aggregations) > 0 {
		return result, fmt.Errorf("convert native source projections separately before importing logical conditions")
	}
	return result, nil
}

func (d Definition) searchCondition(c *esdsl.Condition, direction Direction) error {
	if c.Op == esdsl.OpNested {
		return fmt.Errorf("nested conditions require a reversible nested layout")
	}
	if c.Field != "" {
		mapped, field, err := d.resolve(c.Field, direction)
		if err != nil {
			return err
		}
		canonical := c.Field
		if direction == ToCanonical {
			canonical = mapped
		}
		c.Field = mapped
		if c.Op == esdsl.OpRange {
			if err := field.validateOrdering(); err != nil {
				return err
			}
		}
		unset := false
		operands := []*esdsl.Value{c.Value, c.Gt, c.Gte, c.Lt, c.Lte}
		for i := range c.Values {
			operands = append(operands, &c.Values[i])
		}
		for _, value := range operands {
			if value == nil {
				continue
			}
			if value.Param != "" {
				if field.Unit != "" && field.Unit != "ms" || len(field.Values) > 0 {
					return fmt.Errorf("parameter %s uses source-specific values; author its canonical units explicitly", value.Param)
				}
				continue
			}
			converted, err := field.value(value.Literal, canonical, direction)
			if err != nil {
				return err
			}
			if direction == ToCanonical {
				unset = unset || fmt.Sprint(converted) == "unset"
			} else {
				unset = unset || fmt.Sprint(value.Literal) == "unset"
			}
			value.Literal = converted
		}
		if canonical == "status" && unset && (c.Op == esdsl.OpTerm || c.Op == esdsl.OpTerms) {
			condition := *c
			condition.Occur = esdsl.OccurFilter
			exists := esdsl.Condition{Op: esdsl.OpExists, Field: mapped}
			wrapper := esdsl.Condition{Op: esdsl.OpBool, Occur: c.Occur, When: c.When, Optional: c.Optional}
			if direction == ToSource {
				condition.Occur = esdsl.OccurShould
				exists.Occur = esdsl.OccurMustNot
				wrapper.Conditions = []esdsl.Condition{condition, {Op: esdsl.OpBool, Occur: esdsl.OccurShould, Conditions: []esdsl.Condition{exists}}}
				wrapper.MinimumShouldMatch = "1"
			} else {
				wrapper.Conditions = []esdsl.Condition{condition, exists}
			}
			*c = wrapper
			return nil
		}
	}
	for i, name := range c.Fields {
		mapped, _, err := d.resolve(name, direction)
		if err != nil {
			return err
		}
		c.Fields[i] = mapped
	}
	for i := range c.Conditions {
		if err := d.searchCondition(&c.Conditions[i], direction); err != nil {
			return err
		}
	}
	return nil
}
