package layout

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/flanksource/commons-db/query/esdsl"
)

// FlattenedAttributes maps every canonical name under Prefix to one field: the
// rest of the name, its "." replaced by Separator, under FieldPrefix. Jaeger's
// tags_as_fields writes span tag http.method as tag.http@method.
type FlattenedAttributes struct {
	Prefix      string `json:"prefix"`
	FieldPrefix string `json:"fieldPrefix"`
	Separator   string `json:"separator"`
}

// flattenedAttributeFields are the tag-field objects Jaeger's tags_as_fields
// keeps span and process attributes in.
var flattenedAttributeFields = []FlattenedAttributes{
	{Prefix: "attributes.", FieldPrefix: "tag.", Separator: "@"},
	{Prefix: "resource.", FieldPrefix: "process.tag.", Separator: "@"},
}

// discoverFlattened finds the tag-field objects the index maps, then validates
// any flattened rule declared by the connection layout.
func discoverFlattened(options Discovery) ([]FlattenedAttributes, error) {
	mapsObject := func(rule FlattenedAttributes) bool {
		return options.Fields[strings.TrimSuffix(rule.FieldPrefix, ".")] == "object"
	}
	rules := []FlattenedAttributes{}
	for _, rule := range flattenedAttributeFields {
		if mapsObject(rule) {
			rules = append(rules, rule)
		}
	}
	for _, rule := range options.FlattenedOverrides {
		if rule.Prefix != "attributes." && rule.Prefix != "resource." {
			return nil, fmt.Errorf("flattened layout prefix %q must be %q or %q", rule.Prefix, "attributes.", "resource.")
		}
		if rule.Separator == "" {
			return nil, fmt.Errorf("flattened layout %s requires a separator", rule.Prefix)
		}
		if !strings.HasSuffix(rule.FieldPrefix, ".") || !mapsObject(rule) {
			return nil, fmt.Errorf("flattened layout %s requires field prefix %q to end in \".\" and name an object the index maps", rule.Prefix, rule.FieldPrefix)
		}
		rules = slices.DeleteFunc(rules, func(existing FlattenedAttributes) bool { return existing.Prefix == rule.Prefix })
		rules = append(rules, rule)
	}
	return rules, nil
}

func (d Definition) flattenedRule(canonical string) (FlattenedAttributes, bool) {
	for _, rule := range d.Flattened {
		if strings.HasPrefix(canonical, rule.Prefix) && len(canonical) > len(rule.Prefix) {
			return rule, true
		}
	}
	return FlattenedAttributes{}, false
}

func (rule FlattenedAttributes) field(canonical string) string {
	return rule.FieldPrefix + strings.ReplaceAll(strings.TrimPrefix(canonical, rule.Prefix), ".", rule.Separator)
}

func (f Field) queryField() string {
	if f.Query != "" {
		return f.Query
	}
	return f.Source
}

// AttributeCondition is op on attribute key of a span or of its resource, in
// source fields: one clause per place the layout holds attributes.<key> or
// resource.<key>, any of which may match. OpExists takes no values, OpTerm and
// OpPrefix one, and OpTerms one or more. It fails when neither scope resolves,
// or when a value condition would land on a field that is not an exact keyword.
func (d Definition) AttributeCondition(key string, op esdsl.Operator, values ...string) (esdsl.Condition, error) {
	if err := attributeOperands(op, values); err != nil {
		return esdsl.Condition{}, fmt.Errorf("attribute %q: %w", key, err)
	}
	should := []esdsl.Condition{}
	for _, prefix := range []string{"attributes.", "resource."} {
		canonical := prefix + key
		for _, target := range d.attributeTargets(canonical) {
			clause, err := d.targetCondition(canonical, target, op, values)
			if err != nil {
				return esdsl.Condition{}, err
			}
			clause.Occur = esdsl.OccurShould
			should = append(should, clause)
		}
	}
	if len(should) == 0 {
		return esdsl.Condition{}, fmt.Errorf("attribute %q has no source field, nested list or flattened rule in this span layout", key)
	}
	return esdsl.Condition{Op: esdsl.OpBool, Conditions: should}, nil
}

func attributeOperands(op esdsl.Operator, values []string) error {
	want := map[esdsl.Operator]string{esdsl.OpExists: "no values", esdsl.OpTerm: "one value", esdsl.OpPrefix: "one value", esdsl.OpTerms: "values"}[op]
	switch {
	case want == "":
		return fmt.Errorf("operator %q has no attribute condition", op)
	case op == esdsl.OpExists && len(values) > 0, (op == esdsl.OpTerm || op == esdsl.OpPrefix) && len(values) != 1, op == esdsl.OpTerms && len(values) == 0:
		return fmt.Errorf("operator %q takes %s, not %d", op, want, len(values))
	}
	return nil
}

func (d Definition) targetCondition(canonical string, target Target, op esdsl.Operator, values []string) (esdsl.Condition, error) {
	if err := esdsl.ValidateFieldName(target.Field); err != nil {
		return esdsl.Condition{}, fmt.Errorf("%s: %w", canonical, err)
	}
	// A flattened rule with its own separator names a field Discover keys under
	// a different canonical name, so the mapping is found by the field itself.
	field, mapped := d.Fields[canonical]
	if !mapped && target.Nested == "" {
		field, mapped = d.fieldBySource(target.Field)
	}
	if mapped && op != esdsl.OpExists && field.Type != "keyword" && field.Query == "" {
		return esdsl.Condition{}, fmt.Errorf("%s is mapped as %s; only a keyword field takes an exact %s condition", canonical, field.Type, op)
	}
	if target.Nested == "" {
		// A text field is matched exactly through its keyword subfield.
		if mapped {
			target.Field = field.queryField()
		}
		return valueCondition(op, target.Field, values), nil
	}
	conditions := []esdsl.Condition{}
	for _, key := range slices.Sorted(maps.Keys(target.Where)) {
		conditions = append(conditions, esdsl.Condition{Op: esdsl.OpTerm, Field: key, Value: esdsl.Literal(target.Where[key])})
	}
	if op != esdsl.OpExists {
		conditions = append(conditions, valueCondition(op, target.Field, values))
	}
	return esdsl.Condition{Op: esdsl.OpNested, Path: target.Nested, Conditions: conditions}, nil
}

func (d Definition) fieldBySource(name string) (Field, bool) {
	for _, field := range d.Fields {
		if field.Source == name || field.Query == name {
			return field, true
		}
	}
	return Field{}, false
}

func valueCondition(op esdsl.Operator, field string, values []string) esdsl.Condition {
	switch op {
	case esdsl.OpExists:
		return esdsl.Condition{Op: op, Field: field}
	case esdsl.OpTerms:
		literals := make([]esdsl.Value, len(values))
		for i, value := range values {
			literals[i] = *esdsl.Literal(value)
		}
		return esdsl.Condition{Op: op, Field: field, Values: literals}
	default:
		return esdsl.Condition{Op: op, Field: field, Value: esdsl.Literal(values[0])}
	}
}
