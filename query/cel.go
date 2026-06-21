package query

import (
	"fmt"

	"github.com/flanksource/gomplate/v3"

	"github.com/flanksource/commons-db/context"
)

// applyColumns evaluates each column's CEL expression (when set) against every
// row, writing the computed value back to the row under the column name. Columns
// without a CEL expression are left untouched (the provider already populated
// them).
//
// The current row is exposed to CEL as `row`.
func applyColumns(ctx context.Context, columns []ColumnDef, rows []Row) error {
	celColumns := make([]ColumnDef, 0, len(columns))
	for _, col := range columns {
		if col.CEL != "" {
			celColumns = append(celColumns, col)
		}
	}
	if len(celColumns) == 0 {
		return nil
	}

	for i, row := range rows {
		for _, col := range celColumns {
			val, err := evalColumnCEL(ctx, col.CEL, row)
			if err != nil {
				return fmt.Errorf("column %q: row %d: %w", col.Name, i, err)
			}
			row[col.Name] = val
		}
	}

	return nil
}

// evalColumnCEL evaluates a single CEL expression with the row bound to `row`,
// returning the typed result. The commons-db CEL environment functions are
// injected so expressions can use the same helpers as the rest of the platform.
func evalColumnCEL(ctx context.Context, expr string, row Row) (any, error) {
	t := gomplate.Template{Expression: expr}
	for _, f := range context.CelEnvFuncs {
		t.CelEnvs = append(t.CelEnvs, f(ctx))
	}

	return gomplate.RunExpressionContext(ctx.Context, map[string]any{"row": row}, t)
}
