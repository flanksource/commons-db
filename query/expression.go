package query

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/flanksource/commons-db/context"
)

// ExpressionScope names the binding environment a profile's CEL is evaluated in.
//
// A profile carries expressions in three different environments and the document
// gives no sign of which is which — `columns[].cel` and
// `processors[].config.set.x` are two strings in the same YAML that compile
// against different worlds. Anything offering to evaluate one has to be told
// which it is holding.
type ExpressionScope string

const (
	// ScopeRow is cel.go's environment: `row`, `span`, and every row key that is
	// a valid identifier bound bare. Used by columns, aliases, filters, styles,
	// replay and the reconcile key.
	ScopeRow ExpressionScope = "row"

	// ScopeBatch is the merge environment: `batch`, `first`, `last`, `count` and
	// the kept `row`. Used by a processor's `set`, `when` and `emit`.
	ScopeBatch ExpressionScope = "batch"

	// ScopeBoundary is the grouping environment: `row`, `prev` and `index`. Used
	// by a batch processor's `continuation` and `boundary` predicates.
	ScopeBoundary ExpressionScope = "boundary"
)

// ExpressionOptions describes what an expression is being evaluated against.
type ExpressionOptions struct {
	// Scope selects the binding environment.
	Scope ExpressionScope

	// Rows are the rows the caller already holds. In ScopeRow and ScopeBoundary
	// each row produces a result; in ScopeBatch they are one group producing one.
	Rows []Row

	// Keep chooses the kept row of a ScopeBatch group: "first" (default) or
	// "last".
	Keep string
}

// ExpressionResult is one evaluation, successful or not.
//
// A failure is a field rather than a returned error because a half-written
// expression is the normal state of the input this serves, and because the
// per-row outcome is the entire point: `applyRowTransforms` aborts a whole
// sample on the first row a column expression throws on, so an author asking
// "does this work" currently learns only that some row did not.
type ExpressionResult struct {
	// Index is the row this result came from, or 0 for a batch.
	Index int `json:"index"`

	Value any    `json:"value,omitempty"`
	Type  string `json:"type,omitempty"`
	Error string `json:"error,omitempty"`
}

// EvalExpression evaluates expression once per row, or once per group for
// ScopeBatch, and reports each outcome separately.
//
// The rows travel in from the caller because they came out of /profile/sample
// moments earlier. Re-running someone's backend query on every keystroke to
// fetch rows the caller is already holding would make the preview cost money.
func EvalExpression(ctx context.Context, expression string, options ExpressionOptions) ([]ExpressionResult, error) {
	if strings.TrimSpace(expression) == "" {
		return nil, fmt.Errorf("expression is empty")
	}
	switch options.Scope {
	case ScopeRow, ScopeBatch, ScopeBoundary:
	default:
		return nil, fmt.Errorf("unknown expression scope %q (want %q, %q or %q)",
			options.Scope, ScopeRow, ScopeBatch, ScopeBoundary)
	}
	if len(options.Rows) == 0 {
		return nil, nil
	}

	if options.Scope == ScopeRow {
		return evalRowScope(ctx, expression, options.Rows), nil
	}
	return evalFixedScope(ctx, expression, options)
}

// evalRowScope uses the same evaluator the columns and aliases do, so an
// expression that previews cleanly here behaves identically when the profile
// runs — including the key flattening, which no other scope performs.
func evalRowScope(ctx context.Context, expression string, rows []Row) []ExpressionResult {
	results := make([]ExpressionResult, 0, len(rows))
	for index, row := range rows {
		value, err := evalRowCEL(ctx, expression, row)
		results = append(results, newExpressionResult(index, value, err))
	}
	return results
}

// evalFixedScope compiles once for the batch and boundary environments, which is
// what RowExpr exists for: their binding set is fixed, so the program survives
// gomplate's cache across every row.
func evalFixedScope(ctx context.Context, expression string, options ExpressionOptions) ([]ExpressionResult, error) {
	compiled, err := CompileRowExpr(ctx, expression)
	if err != nil {
		return nil, err
	}

	if options.Scope == ScopeBatch {
		value, err := compiled.Eval(MergeScope(options.Rows, options.Keep))
		return []ExpressionResult{newExpressionResult(0, value, err)}, nil
	}

	// The first row is never judged: it starts a batch unconditionally, and
	// `startsBatch` is only reached once there is a row above. Returning a
	// fabricated verdict for it would preview a decision the engine never makes.
	results := make([]ExpressionResult, 0, max(len(options.Rows)-1, 0))
	for index := 1; index < len(options.Rows); index++ {
		value, err := compiled.Eval(BoundaryScope(options.Rows, index))
		results = append(results, newExpressionResult(index, value, err))
	}
	return results, nil
}

func newExpressionResult(index int, value any, err error) ExpressionResult {
	if err != nil {
		return ExpressionResult{Index: index, Error: err.Error()}
	}
	value = normalizeNull(value)
	return ExpressionResult{Index: index, Value: value, Type: CELTypeName(value)}
}

// normalizeNull collapses CEL's null onto Go's.
//
// A missing field or an out-of-range index does not throw here — gomplate's
// nilsafe library folds it into `structpb.NullValue`, whose Go value is the
// integer 0. Left alone it would reach a caller as a plain zero, so an
// expression that read nothing would preview as a column of confident zeroes.
func normalizeNull(value any) any {
	if value == structpb.NullValue_NULL_VALUE {
		return nil
	}
	return value
}

// MergeScope is the CEL environment a group expression is evaluated in: the
// bindings a processor's `set`, `when` and `emit` see.
//
// It lives here rather than in the processor package so that previewing one of
// those expressions and running it cannot drift apart.
func MergeScope(group []Row, keep string) map[string]any {
	if len(group) == 0 {
		return map[string]any{"batch": []any{}, "count": 0}
	}
	rows := make([]any, len(group))
	for index, row := range group {
		rows[index] = map[string]any(row)
	}
	return map[string]any{
		"batch": rows,
		"first": map[string]any(group[0]),
		"last":  map[string]any(group[len(group)-1]),
		"count": len(group),
		"row":   map[string]any(KeptRow(group, keep)),
	}
}

// BoundaryScope is the environment a grouping predicate sees for one row: the
// row, the row above it, and its position.
//
// All three are always bound, matching `startsBatch`. Omitting one would not
// read as null — gomplate declares its variables from this map, so an absent
// binding makes the expression fail to compile with "undeclared reference"
// rather than evaluate to nothing.
func BoundaryScope(rows []Row, index int) map[string]any {
	previous := Row{}
	if index > 0 {
		previous = rows[index-1]
	}
	return map[string]any{
		"row":   map[string]any(rows[index]),
		"prev":  map[string]any(previous),
		"index": index,
	}
}

// Keep values selecting which row of a group survives the merge.
const (
	KeepFirst = "first"
	KeepLast  = "last"
)

// KeptRow returns the row a merge builds its output from.
func KeptRow(group []Row, keep string) Row {
	if keep == KeepLast {
		return group[len(group)-1]
	}
	return group[0]
}

// CELTypeName names a result the way an author reads it, so a column declared
// `type: number` can be checked against what its expression actually returns.
func CELTypeName(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case string:
		return "string"
	case int, int32, int64:
		return "int"
	case uint, uint32, uint64:
		return "uint"
	case float32, float64:
		return "double"
	case []any:
		return "list"
	case map[string]any:
		return "map"
	default:
		return fmt.Sprintf("%T", value)
	}
}
