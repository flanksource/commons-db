package esdsl

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
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
func bindSearch(search Search, params []ParamBinding, referenced []string) (root *bound, size int, from int, uses []ParamUse, err error) {
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

	timeRange, err := b.bindTimeRange(search.TimeField)
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
func (b *binder) bindTimeRange(timeField string) (*bound, error) {
	from, hasFrom := b.roleValue(RoleTimeFrom)
	to, hasTo := b.roleValue(RoleTimeTo)
	if !hasFrom && !hasTo {
		return nil, nil
	}
	if timeField == "" {
		return nil, fmt.Errorf("a time-from/time-to parameter requires timeField on the search specification")
	}
	if err := ValidateFieldName(timeField); err != nil {
		return nil, fmt.Errorf("timeField: %w", err)
	}
	node := &bound{spec: Condition{Op: OpRange, Field: timeField}}
	if hasFrom {
		node.gte = &boundValue{value: from, fromParam: true}
	}
	if hasTo {
		node.lte = &boundValue{value: to, fromParam: true}
	}
	return node, nil
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
