package query

import (
	"fmt"
	"reflect"

	"github.com/flanksource/gomplate/v3"
	"github.com/google/cel-go/common/types/ref"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/flanksource/commons-db/context"
)

// RowExpr is a CEL expression evaluated against an explicit, caller-fixed set of
// bindings.
//
// It deliberately differs from the column and alias expressions in cel.go, which
// flatten every row key into a top-level variable. Those run once per profile
// column; batch predicates run once per row, and a per-row variable set both
// defeats gomplate's compiled-program cache (its key includes the variable
// names) and turns a sparse row into an "undeclared reference" failure halfway
// through a scan. With a fixed binding set the program is compiled once and a
// missing field reads as null, which gomplate's nilsafe library then folds into
// the zero value of whatever it is used as.
type RowExpr struct {
	expression string
	template   gomplate.Template
	ctx        context.Context
}

// CompileRowExpr prepares expression for repeated evaluation. CEL compilation
// itself happens on the first Eval, inside gomplate's program cache.
func CompileRowExpr(ctx context.Context, expression string) (*RowExpr, error) {
	if expression == "" {
		return nil, fmt.Errorf("expression is empty")
	}
	template := gomplate.Template{Expression: expression}
	for _, function := range context.CelEnvFuncs {
		template.CelEnvs = append(template.CelEnvs, function(ctx))
	}
	return &RowExpr{expression: expression, template: template, ctx: ctx}, nil
}

// Expression returns the source text, for error messages.
func (e *RowExpr) Expression() string { return e.expression }

// Eval runs the expression with bindings as the variable environment.
func (e *RowExpr) Eval(bindings map[string]any) (any, error) {
	value, err := gomplate.RunExpressionContext(e.ctx.Context, bindings, e.template)
	if err != nil {
		return nil, fmt.Errorf("cel %q: %w", e.expression, err)
	}
	return value, nil
}

// Bool evaluates the expression as a predicate. A non-boolean result is an
// error rather than a truthiness guess, so a typo like `count` instead of
// `count > 1` fails loudly.
func (e *RowExpr) Bool(bindings map[string]any) (bool, error) {
	value, err := e.Eval(bindings)
	if err != nil {
		return false, err
	}
	if value == nil || value == structpb.NullValue_NULL_VALUE {
		return false, fmt.Errorf(
			"cel %q: evaluated to null, which means a field it reads is absent on this row; make the expression null-safe, e.g. (row.message + \"\").matches(...)",
			e.expression)
	}
	result, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("cel %q: expected a boolean, got %T (%v)", e.expression, value, value)
	}
	return result, nil
}

// Rows evaluates the expression as a list of rows.
//
// A list CEL built itself — anything downstream of `map()` — comes back as
// []ref.Val holding CEL values rather than as plain Go maps, so each element is
// converted rather than type-asserted.
func (e *RowExpr) Rows(bindings map[string]any) ([]Row, error) {
	value, err := e.Eval(bindings)
	if err != nil {
		return nil, err
	}
	var elements []any
	switch list := value.(type) {
	case []any:
		elements = list
	case []ref.Val:
		elements = make([]any, len(list))
		for index, item := range list {
			elements[index] = item
		}
	default:
		return nil, fmt.Errorf("cel %q: expected a list of rows, got %T", e.expression, value)
	}

	rows := make([]Row, 0, len(elements))
	for index, element := range elements {
		row, err := toRow(element)
		if err != nil {
			return nil, fmt.Errorf("cel %q: element %d: %w", e.expression, index, err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

var rowType = reflect.TypeOf(map[string]any{})

func toRow(value any) (Row, error) {
	switch typed := value.(type) {
	case map[string]any:
		return Row(typed), nil
	case ref.Val:
		native, err := typed.ConvertToNative(rowType)
		if err != nil {
			return nil, fmt.Errorf("%v is not a row: %w", typed.Type(), err)
		}
		return Row(native.(map[string]any)), nil
	default:
		return nil, fmt.Errorf("%T is not a row", value)
	}
}
