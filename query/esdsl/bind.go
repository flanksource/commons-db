package esdsl

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/commons-db/query/datetime"
)

// ParamBinding is one resolved profile parameter handed to Compile. Role
// mirrors query.ParamRole; an empty role behaves as RoleFilter.
type ParamBinding struct {
	Name  string
	Role  string
	Value any
}

// boundValue is an operand after parameter substitution. fromParam records that
// the value was supplied at request time, which is what drives escaping.
type boundValue struct {
	value     any
	fromParam bool
}

// bound is a condition whose operands have been resolved and whose optional
// branches have been pruned.
type bound struct {
	spec             Condition
	value            *boundValue
	values           []boundValue
	gt, gte, lt, lte *boundValue
	children         []bound
}

type binder struct {
	params map[string]ParamBinding
	used   map[string]bool
	uses   []ParamUse
}

// bindSearch resolves every parameter reference in the specification, folds
// role-carrying parameters into native constructs, and reports the resolved
// size and from. A nil root means "match every document". referenced names
// parameters already consumed by the caller, which count as used here.
func bindSearch(
	search Search,
	params []ParamBinding,
	referenced []string,
	timeFieldMapping *TimeFieldMapping,
) (root *bound, size int, from int, uses []ParamUse, err error) {
	b := &binder{params: make(map[string]ParamBinding, len(params)), used: map[string]bool{}}
	for _, name := range referenced {
		b.used[name] = true
	}
	for _, param := range params {
		if param.Name == "" {
			return nil, 0, 0, nil, fmt.Errorf("parameter binding is missing a name")
		}
		if _, duplicate := b.params[param.Name]; duplicate {
			return nil, 0, 0, nil, fmt.Errorf("parameter %q is bound twice", param.Name)
		}
		b.params[param.Name] = param
	}

	var tree *bound
	if search.Query != nil {
		node, keep, bindErr := b.bindCondition(*search.Query, "query")
		if bindErr != nil {
			return nil, 0, 0, nil, bindErr
		}
		if keep {
			tree = &node
		}
	}

	timeRange, err := b.bindTimeRange(search, timeFieldMapping)
	if err != nil {
		return nil, 0, 0, nil, err
	}
	tree = attachFilter(tree, timeRange)

	size, err = b.resolveCount(RoleLimit, search.Size, "limit")
	if err != nil {
		return nil, 0, 0, nil, err
	}
	from, err = b.resolveCount(RoleOffset, search.From, "offset")
	if err != nil {
		return nil, 0, 0, nil, err
	}

	if err := b.assertAllUsed(); err != nil {
		return nil, 0, 0, nil, err
	}
	return tree, size, from, b.uses, nil
}

// attachFilter adds extra as a filter clause of tree, wrapping a leaf root in a
// bool when needed. A match_all root is dropped rather than carried alongside:
// in filter context it selects nothing extra, and keeping it only clutters the
// DSL the builder shows back to the author.
func attachFilter(tree *bound, extra *bound) *bound {
	if extra == nil {
		return tree
	}
	extra.spec.Occur = OccurFilter
	if tree == nil || tree.spec.Op == OpMatchAll {
		return &bound{spec: Condition{Op: OpBool}, children: []bound{*extra}}
	}
	if tree.spec.Op == OpBool {
		tree.children = append(tree.children, *extra)
		return tree
	}
	root := *tree
	root.spec.Occur = OccurFilter
	return &bound{spec: Condition{Op: OpBool}, children: []bound{root, *extra}}
}

// bindTimeRange folds time-from and time-to parameters into one range clause on
// the specification's time field.
func (b *binder) bindTimeRange(search Search, mapping *TimeFieldMapping) (*bound, error) {
	from, hasFrom := b.roleValue(RoleTimeFrom)
	to, hasTo := b.roleValue(RoleTimeTo)
	if !hasFrom && !hasTo {
		return nil, nil
	}
	if search.TimeField == "" {
		return nil, fmt.Errorf("a time-from/time-to parameter requires timeField on the search specification")
	}
	if err := ValidateFieldName(search.TimeField); err != nil {
		return nil, fmt.Errorf("timeField: %w", err)
	}
	node := &bound{spec: Condition{Op: OpRange, Field: search.TimeField}}
	if hasFrom {
		value, _, err := normalizeTimeBound(from, RoleTimeFrom, search, mapping)
		if err != nil {
			return nil, err
		}
		node.gte = &boundValue{value: value, fromParam: true}
	}
	if hasTo {
		value, exclusive, err := normalizeTimeBound(to, RoleTimeTo, search, mapping)
		if err != nil {
			return nil, err
		}
		if exclusive {
			node.lt = &boundValue{value: value, fromParam: true}
		} else {
			node.lte = &boundValue{value: value, fromParam: true}
		}
	}
	return node, nil
}

func normalizeTimeBound(raw any, role string, search Search, mapping *TimeFieldMapping) (any, bool, error) {
	if mapping == nil {
		return raw, false, nil
	}
	if err := validateTimeFieldMapping(search, mapping.Type); err != nil {
		return nil, false, err
	}
	input := fmt.Sprint(raw)
	parsed, err := datetime.Parse(input, mapping.Now)
	if err != nil {
		return nil, false, fmt.Errorf("%s parameter for timeField %q: %w", role, search.TimeField, err)
	}
	exclusive := role == RoleTimeTo && parsed.DateOnly
	if exclusive {
		parsed.Time = parsed.Time.UTC().AddDate(0, 0, 1)
	}
	if parsed.DateMath && (mapping.Type == "date" || mapping.Type == "date_nanos") {
		// Date math is left for OpenSearch to resolve, which it can only do on a
		// real date field.
		return input, false, nil
	}
	encoded, err := EncodeTimeBound(parsed.Time, search, mapping)
	if err != nil {
		return nil, false, fmt.Errorf("%s parameter for timeField %q: %w", role, search.TimeField, err)
	}
	return encoded, exclusive, nil
}

// EncodeTimeBound renders instant as a value comparable against search.TimeField
// under mapping: an RFC3339 string for a date field, an epoch integer in the
// declared unit for a numeric one.
//
// It is exported because a tail bounds its polls at an instant of its own
// choosing (see the OpenSearch provider's tailLag) rather than at a parameter
// somebody supplied. Encoding that instant anywhere else would be a second
// answer to "how does this index spell a time", and the two would disagree the
// day one of them learned a new mapping type.
func EncodeTimeBound(instant time.Time, search Search, mapping *TimeFieldMapping) (any, error) {
	if mapping == nil {
		return nil, fmt.Errorf("timeField %q has no resolved mapping to encode against", search.TimeField)
	}
	if err := validateTimeFieldMapping(search, mapping.Type); err != nil {
		return nil, err
	}
	if mapping.Type == "date" || mapping.Type == "date_nanos" {
		formats := strings.Split(mapping.Format, "||")
		for _, format := range formats {
			switch strings.TrimSpace(format) {
			case "", "date_optional_time", "strict_date_optional_time", "date_optional_time_nanos", "strict_date_optional_time_nanos":
				return instant.UTC().Format(time.RFC3339Nano), nil
			}
		}
		for _, format := range formats {
			switch strings.TrimSpace(format) {
			case string(TimeFieldFormatEpochSecond):
				return instant.Unix(), nil
			case string(TimeFieldFormatEpochMillis):
				return instant.UnixMilli(), nil
			}
		}
		return nil, fmt.Errorf("timeField %q is mapped as %q with unsupported date format %q", search.TimeField, mapping.Type, mapping.Format)
	}
	encoded := encodeEpoch(instant, search.TimeFieldFormat)
	if err := validateEpochRange(mapping.Type, encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func validateTimeFieldMapping(search Search, mappedType string) error {
	switch mappedType {
	case "date", "date_nanos":
		if search.TimeFieldFormat != "" {
			return fmt.Errorf("timeField %q is mapped as %q and must not set timeFieldFormat", search.TimeField, mappedType)
		}
	case "byte", "short", "integer", "long", "unsigned_long":
		if search.TimeFieldFormat == "" {
			return fmt.Errorf("timeField %q is mapped as %q and requires timeFieldFormat", search.TimeField, mappedType)
		}
	default:
		return fmt.Errorf("timeField %q has incompatible OpenSearch mapping type %q", search.TimeField, mappedType)
	}
	return nil
}

func encodeEpoch(value time.Time, format TimeFieldFormat) int64 {
	switch format {
	case TimeFieldFormatEpochSecond:
		return value.Unix()
	case TimeFieldFormatEpochMillis:
		return value.UnixMilli()
	case TimeFieldFormatEpochMicros:
		return value.UnixMicro()
	default:
		return value.UnixNano()
	}
}

func validateEpochRange(mappedType string, value int64) error {
	switch mappedType {
	case "byte":
		if value < math.MinInt8 || value > math.MaxInt8 {
			return fmt.Errorf("epoch value %d overflows byte", value)
		}
	case "short":
		if value < math.MinInt16 || value > math.MaxInt16 {
			return fmt.Errorf("epoch value %d overflows short", value)
		}
	case "integer":
		if value < math.MinInt32 || value > math.MaxInt32 {
			return fmt.Errorf("epoch value %d overflows integer", value)
		}
	case "unsigned_long":
		if value < 0 {
			return fmt.Errorf("epoch value %d is negative for unsigned_long", value)
		}
	}
	return nil
}

// resolveCount returns the value of the role-carrying parameter when present,
// otherwise the specification's own value. Zero means unspecified.
func (b *binder) resolveCount(role string, specValue *int, label string) (int, error) {
	if value, ok := b.roleValue(role); ok {
		count, err := toInt(value)
		if err != nil {
			return 0, fmt.Errorf("%s parameter: %w", label, err)
		}
		if count < 0 {
			return 0, fmt.Errorf("%s parameter must not be negative, got %d", label, count)
		}
		return count, nil
	}
	if specValue != nil {
		return *specValue, nil
	}
	return 0, nil
}

// roleValue returns the first non-empty parameter carrying role, marking it used.
func (b *binder) roleValue(role string) (any, bool) {
	for _, name := range b.sortedNames() {
		param := b.params[name]
		if param.Role != role || isEmptyValue(param.Value) {
			continue
		}
		b.used[param.Name] = true
		return param.Value, true
	}
	return nil, false
}

// assertAllUsed fails when a supplied filter parameter is referenced nowhere in
// the specification, so a typo surfaces instead of silently widening the query.
func (b *binder) assertAllUsed() error {
	for _, name := range b.sortedNames() {
		param := b.params[name]
		if b.used[name] || isEmptyValue(param.Value) {
			continue
		}
		if param.Role != "" && param.Role != RoleFilter {
			continue
		}
		return fmt.Errorf(
			"param %q is not referenced by the search specification: bind it as {\"param\":%q} or interpolate it as {{.params.%s}}",
			name, name, name)
	}
	return nil
}

func (b *binder) sortedNames() []string {
	names := make([]string, 0, len(b.params))
	for name := range b.params {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// gateOpen reports whether the parameter named by a condition's `when` resolved
// to something. The value itself is never read — supplying the parameter at all
// is what turns the condition on — so it counts as used either way.
func (b *binder) gateOpen(name string) bool {
	param, ok := b.params[name]
	if !ok {
		return false
	}
	b.used[name] = true
	return !isEmptyValue(param.Value)
}

// resolve substitutes a parameter reference. present is false when the operand
// resolves to nothing, which prunes an optional condition.
func (b *binder) resolve(v *Value) (*boundValue, bool) {
	if v == nil {
		return nil, false
	}
	if v.Param == "" {
		if isEmptyValue(v.Literal) {
			return nil, false
		}
		return &boundValue{value: v.Literal}, true
	}
	param, ok := b.params[v.Param]
	if !ok {
		return nil, false
	}
	b.used[v.Param] = true
	if isEmptyValue(param.Value) {
		return nil, false
	}
	return &boundValue{value: param.Value, fromParam: true}, true
}

func (b *binder) bindCondition(c Condition, path string) (bound, bool, error) {
	info, ok := Lookup(c.Op)
	if !ok {
		return bound{}, false, fmt.Errorf("%s: unknown operator %q", path, c.Op)
	}
	if c.When != "" && !b.gateOpen(c.When) {
		return bound{}, false, nil
	}

	node := bound{spec: c}
	node.spec.Conditions = nil

	if info.Group {
		for i := range c.Conditions {
			child, keep, err := b.bindCondition(c.Conditions[i], fmt.Sprintf("%s.conditions[%d]", path, i))
			if err != nil {
				return bound{}, false, err
			}
			if keep {
				node.children = append(node.children, child)
			}
		}
		return node, len(node.children) > 0, nil
	}
	b.recordParamUses(c)

	switch info.Arity {
	case ArityNone:
		return node, true, nil
	case AritySingle:
		value, present := b.resolve(c.Value)
		if !present {
			return b.prune(c, path)
		}
		node.value = value
	case ArityMultiple:
		values, err := b.bindList(c, path)
		if err != nil {
			return bound{}, false, err
		}
		if len(values) == 0 {
			return b.prune(c, path)
		}
		node.values = values
	case ArityRange:
		node.gt, _ = b.resolve(c.Gt)
		node.gte, _ = b.resolve(c.Gte)
		node.lt, _ = b.resolve(c.Lt)
		node.lte, _ = b.resolve(c.Lte)
		if node.gt == nil && node.gte == nil && node.lt == nil && node.lte == nil {
			return b.prune(c, path)
		}
	}
	return node, true, nil
}

func (b *binder) recordParamUses(condition Condition) {
	if condition.Field == "" {
		return
	}
	seen := map[string]bool{}
	for _, name := range paramNames(condition) {
		if seen[name] {
			continue
		}
		if _, supplied := b.params[name]; supplied {
			b.uses = append(b.uses, ParamUse{Name: name, Field: condition.Field})
			seen[name] = true
		}
	}
}

// bindList resolves a multi-operand condition, expanding a single operand that
// carries a list or a comma-separated string.
func (b *binder) bindList(c Condition, path string) ([]boundValue, error) {
	if c.Value != nil {
		resolved, present := b.resolve(c.Value)
		if !present {
			return nil, nil
		}
		items := normalizeList(resolved.value)
		values := make([]boundValue, 0, len(items))
		for _, item := range items {
			values = append(values, boundValue{value: item, fromParam: resolved.fromParam})
		}
		return values, nil
	}
	values := make([]boundValue, 0, len(c.Values))
	for i := range c.Values {
		resolved, present := b.resolve(&c.Values[i])
		if !present {
			continue
		}
		for _, item := range normalizeList(resolved.value) {
			values = append(values, boundValue{value: item, fromParam: resolved.fromParam})
		}
	}
	return values, nil
}

// prune drops an optional condition whose operand resolved to nothing, and
// fails loudly otherwise.
func (b *binder) prune(c Condition, path string) (bound, bool, error) {
	if c.Optional {
		return bound{}, false, nil
	}
	return bound{}, false, fmt.Errorf("%s: %s has no value; declare it optional to drop the condition instead",
		path, describeOperand(c))
}

func describeOperand(c Condition) string {
	names := paramNames(c)
	switch {
	case len(names) == 1:
		return fmt.Sprintf("param %q", names[0])
	case len(names) > 1:
		return fmt.Sprintf("params %s", strings.Join(quoteAll(names), ", "))
	case c.Field != "":
		return fmt.Sprintf("operator %q on field %q", c.Op, c.Field)
	default:
		return fmt.Sprintf("operator %q", c.Op)
	}
}

func paramNames(c Condition) []string {
	var names []string
	add := func(v *Value) {
		if v != nil && v.Param != "" {
			names = append(names, v.Param)
		}
	}
	add(c.Value)
	for i := range c.Values {
		add(&c.Values[i])
	}
	add(c.Gt)
	add(c.Gte)
	add(c.Lt)
	add(c.Lte)
	return names
}

func quoteAll(names []string) []string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = strconv.Quote(name)
	}
	return quoted
}

// normalizeList expands an operand into a list, splitting a comma-separated
// string so a single request parameter can supply several terms.
func normalizeList(value any) []any {
	switch typed := value.(type) {
	case nil:
		return nil
	case []any:
		return typed
	case []string:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return items
	case string:
		items := make([]any, 0, 1)
		for _, part := range strings.Split(typed, ",") {
			if part = strings.TrimSpace(part); part != "" {
				items = append(items, part)
			}
		}
		return items
	default:
		return []any{typed}
	}
}

func isEmptyValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	case []string:
		return len(typed) == 0
	default:
		return false
	}
}

func toInt(value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case float32:
		return int(typed), nil
	case float64:
		return int(typed), nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, fmt.Errorf("value %q is not a whole number", typed)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("value %v is not a whole number", value)
	}
}
