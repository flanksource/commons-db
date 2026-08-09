package processor

import (
	"fmt"
	"sort"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// merger collapses a group of rows into one. It is deliberately ignorant of how
// the group was formed: "cel.batch" builds groups from adjacent runs and
// "cel.dedupe" from a hash of the partition key, but both then answer the same
// question — given these rows, what single row replaces them?
type merger struct {
	keep     string
	when     *query.RowExpr
	emit     *query.RowExpr
	set      map[string]*query.RowExpr
	setOrder []string
}

// compileMerger compiles the expressions once, so a result of a million rows
// pays for parsing them a million times over.
func compileMerger(ctx context.Context, keep, when, emit string, set map[string]string) (*merger, error) {
	compiled := &merger{keep: keep, set: map[string]*query.RowExpr{}}

	for _, expression := range []struct {
		source string
		target **query.RowExpr
	}{
		{when, &compiled.when},
		{emit, &compiled.emit},
	} {
		if expression.source == "" {
			continue
		}
		expr, err := query.CompileRowExpr(ctx, expression.source)
		if err != nil {
			return nil, err
		}
		*expression.target = expr
	}

	for name := range set {
		compiled.setOrder = append(compiled.setOrder, name)
	}
	sort.Strings(compiled.setOrder)
	for _, name := range compiled.setOrder {
		expr, err := query.CompileRowExpr(ctx, set[name])
		if err != nil {
			return nil, fmt.Errorf("set %q: %w", name, err)
		}
		compiled.set[name] = expr
	}

	return compiled, nil
}

// collapse reduces one group to its merged form. A group the When gate rejects
// passes through untouched, which is how `count > 1` leaves ordinary single
// rows alone.
func (m *merger) collapse(group []query.Row) ([]query.Row, error) {
	scope := mergeScope(group, m.keep)
	if m.when != nil {
		selected, err := m.when.Bool(scope)
		if err != nil {
			return nil, err
		}
		if !selected {
			return group, nil
		}
	}
	if m.emit != nil {
		return m.emit.Rows(scope)
	}

	// Every expression sees the kept row as it arrived, so the merged row does
	// not depend on the order Set happens to be walked in.
	values := make(map[string]any, len(m.set))
	for _, name := range m.setOrder {
		value, err := m.set[name].Eval(scope)
		if err != nil {
			return nil, fmt.Errorf("set %q: %w", name, err)
		}
		values[name] = value
	}

	merged := make(query.Row, len(group[0])+len(values))
	for key, value := range keptRow(group, m.keep) {
		merged[key] = value
	}
	for name, value := range values {
		merged[name] = value
	}
	return []query.Row{merged}, nil
}

// mergeScope is the CEL environment a group expression is evaluated in.
func mergeScope(group []query.Row, keep string) map[string]any {
	rows := make([]any, len(group))
	for index, row := range group {
		rows[index] = map[string]any(row)
	}
	return map[string]any{
		"batch": rows,
		"first": map[string]any(group[0]),
		"last":  map[string]any(group[len(group)-1]),
		"count": len(group),
		"row":   map[string]any(keptRow(group, keep)),
	}
}

func keptRow(group []query.Row, keep string) query.Row {
	if keep == KeepLast {
		return group[len(group)-1]
	}
	return group[0]
}

func partitionKey(row query.Row, columns []string) string {
	if len(columns) == 0 {
		return ""
	}
	parts := make([]string, len(columns))
	for index, column := range columns {
		parts[index] = query.NormalizeKeyValue(row[column])
	}
	return fmt.Sprint(parts)
}
