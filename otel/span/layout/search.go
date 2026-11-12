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
		if list, _, ok := d.nestedTarget(sort.Field, direction); ok {
			return result, fmt.Errorf("sort on %s reads nested %s, which a structured search sort cannot express; sort the compiled query instead", sort.Field, list.Path)
		}
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
		if direction != ToCanonical {
			return fmt.Errorf("nested conditions require a reversible nested layout")
		}
		return d.foldNestedCondition(c)
	}
	if list, key, ok := d.nestedTarget(c.Field, direction); ok && c.Field != "" {
		if c.Op == esdsl.OpRange {
			return fmt.Errorf("range on %s is unsupported: nested %s values are keyword strings and would compare lexically; index a typed field (Jaeger tags-as-fields or an ingest pipeline) to filter it by range", c.Field, list.Path)
		}
		pin := esdsl.Condition{Op: esdsl.OpTerm, Occur: esdsl.OccurFilter, Field: list.Key, Value: esdsl.Literal(key)}
		group := esdsl.Condition{Op: esdsl.OpNested, Path: list.Path, Occur: c.Occur, When: c.When, Optional: c.Optional, Conditions: []esdsl.Condition{pin}}
		if c.Op != esdsl.OpExists {
			leaf := *c
			leaf.Field, leaf.Occur, leaf.When, leaf.Optional = list.Value, esdsl.OccurFilter, "", false
			group.Conditions = append(group.Conditions, leaf)
		}
		*c = group
		return nil
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

// foldNestedCondition restores the canonical leaf searchCondition wrapped into
// a nested group; any other nested group has no canonical meaning.
func (d Definition) foldNestedCondition(c *esdsl.Condition) error {
	list, ok := d.nestedSource(c.Path)
	if !ok {
		return fmt.Errorf("nested path %q is not a canonical attribute list", c.Path)
	}
	if c.ScoreMode != "" || len(c.Conditions) < 1 || len(c.Conditions) > 2 {
		return fmt.Errorf("nested %s group must pin one %s and at most one value condition", c.Path, list.Key)
	}
	pin := c.Conditions[0]
	key, isString := literal(pin.Value).(string)
	if pin.Op != esdsl.OpTerm || pin.Field != list.Key || !isString || key == "" {
		return fmt.Errorf("nested %s group must pin the entry with a term on %s", c.Path, list.Key)
	}
	restored := esdsl.Condition{Op: esdsl.OpExists}
	if len(c.Conditions) == 2 {
		restored = c.Conditions[1]
		if restored.Field != list.Value {
			return fmt.Errorf("nested %s value condition must target %s", c.Path, list.Value)
		}
	}
	restored.Field, restored.Occur, restored.When, restored.Optional = list.Prefix+key, c.Occur, c.When, c.Optional
	*c = restored
	return nil
}

func literal(value *esdsl.Value) any {
	if value == nil {
		return nil
	}
	return value.Literal
}
