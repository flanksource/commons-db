package query

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flanksource/commons-db/context"
)

// keySeparator joins multi-column key parts. NUL cannot appear in a rendered
// cell value, so ["a","b"] and ["a\x00b"] can never collide.
const keySeparator = "\x00"

// KeySpec derives a comparison key from a Row. Exactly one of Columns or CEL
// must be set.
//
// Columns is the right choice when both sides of a comparison share a schema —
// the same profile at two points in time. CEL is required when they do not: a
// join across two profiles reads the same logical identity out of differently
// named or nested fields on each side.
type KeySpec struct {
	// Columns names the row keys whose values, joined in order, form the key.
	Columns []string `json:"columns,omitempty" yaml:"columns,omitempty"`

	// CEL is an expression evaluated against the row. The row is exposed as
	// both `row` and `span`, and every row key that is a valid CEL identifier
	// is bound as a top-level variable.
	CEL string `json:"cel,omitempty" yaml:"cel,omitempty"`
}

// KeyFunc derives the key of a single Row.
type KeyFunc func(Row) (string, error)

// Validate rejects a KeySpec that names neither or both key sources.
func (k KeySpec) Validate() error {
	hasColumns := len(k.Columns) > 0
	hasCEL := strings.TrimSpace(k.CEL) != ""
	switch {
	case !hasColumns && !hasCEL:
		return fmt.Errorf("key requires either columns or a cel expression")
	case hasColumns && hasCEL:
		return fmt.Errorf("key sets both columns and cel; pick one")
	default:
		return nil
	}
}

// Resolver compiles the KeySpec into a per-row key function.
func (k KeySpec) Resolver(ctx context.Context) (KeyFunc, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	if len(k.Columns) > 0 {
		columns := k.Columns
		return func(row Row) (string, error) {
			parts := make([]string, len(columns))
			for i, column := range columns {
				parts[i] = NormalizeKeyValue(row[column])
			}
			return strings.Join(parts, keySeparator), nil
		}, nil
	}
	expression := k.CEL
	return func(row Row) (string, error) {
		value, err := evalRowCEL(ctx, expression, row)
		if err != nil {
			return "", enrichCELError(err, row)
		}
		return NormalizeKeyValue(value), nil
	}, nil
}

// NormalizeKeyValue renders one key component. The CEL and SQL layers spell an
// absent value several different ways; they all collapse to the empty key so a
// missing identity never masquerades as a distinct one.
func NormalizeKeyValue(value any) string {
	if value == nil {
		return ""
	}
	switch rendered := fmt.Sprint(value); rendered {
	case "NULL_VALUE", "null", "<nil>":
		return ""
	default:
		return rendered
	}
}

// enrichCELError appends the row's bindable variable names to an undeclared
// reference error, which is otherwise a dead end when the author cannot see
// which fields the provider actually returned.
func enrichCELError(err error, row Row) error {
	if err == nil || !strings.Contains(err.Error(), "undeclared reference") {
		return err
	}
	names := make([]string, 0, len(row)+2)
	names = append(names, "row", "span")
	for name := range row {
		if celIdentifier.MatchString(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return fmt.Errorf("%w\n\navailable variables: %s", err, strings.Join(names, ", "))
}
