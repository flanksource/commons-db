package query

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/flanksource/gomplate/v3"
	"github.com/ohler55/ojg/jp"

	"github.com/flanksource/commons-db/context"
)

var celIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// applyRowTransforms evaluates aliases, drops filtered rows, then projects
// columns. It returns the surviving rows: filters run after aliases so a
// predicate can read a synthesised field, and before columns so a row on its
// way out is never charged for a projection nobody will read.
//
// The second return holds each surviving row's cell styles, parallel to the
// first and nil when no column declares a Style. Styles ride alongside the rows
// rather than inside them so that presentation never reaches an export.
func applyRowTransforms(ctx context.Context, profile Profile, rows []Row) ([]Row, []map[string]string, error) {
	outputNames := make(map[string]struct{}, len(profile.Columns))
	for _, column := range profile.Columns {
		outputNames[column.Name] = struct{}{}
	}
	jsonPaths := make(map[string]jp.Expr, len(profile.Columns))
	for _, column := range profile.Columns {
		if column.JSONPath == "" {
			continue
		}
		expression, err := compileColumnJSONPath(column)
		if err != nil {
			return nil, nil, err
		}
		jsonPaths[column.Name] = expression
	}
	filters, err := activeFilters(profile.Filters)
	if err != nil {
		return nil, nil, err
	}
	styled := false
	for _, column := range profile.Columns {
		if column.Style != "" {
			styled = true
			break
		}
	}
	kept := rows[:0]
	var styles []map[string]string
	for index, row := range rows {
		projected := make([]struct {
			name  string
			value any
		}, 0, len(profile.Aliases))
		ignoredNames := make(map[string]struct{}, len(profile.Ignore))
		for _, alias := range profile.Aliases {
			if alias.Name == "" || alias.CEL == "" {
				return nil, nil, fmt.Errorf("row %d: alias name and cel are required", index)
			}
			value, err := evalRowCEL(ctx, alias.CEL, row)
			if err != nil {
				return nil, nil, fmt.Errorf("row %d: alias %q: %w", index, alias.Name, err)
			}
			setRowPath(row, alias.Name, value)
			projected = append(projected, struct {
				name  string
				value any
			}{name: alias.Name, value: value})
		}
		for _, ignored := range profile.Ignore {
			ignoredNames[ignored] = struct{}{}
			delete(row, ignored)
			deleteRowPath(row, ignored)
		}
		for _, alias := range projected {
			if _, ignored := ignoredNames[alias.name]; ignored {
				continue
			}
			setRowPath(row, alias.name, alias.value)
		}
		if len(filters) > 0 {
			keep, err := keepRow(ctx, filters, row)
			if err != nil {
				return nil, nil, fmt.Errorf("row %d: %w", index, err)
			}
			if !keep {
				continue
			}
		}
		for _, column := range profile.Columns {
			switch {
			case column.CEL != "":
				value, err := evalRowCEL(ctx, column.CEL, row)
				if err != nil {
					return nil, nil, fmt.Errorf("row %d: column %q: %w", index, column.Name, err)
				}
				row[column.Name] = value
			case column.JSONPath != "":
				value, err := evalRowJSONPath(jsonPaths[column.Name], column.Source, row)
				if err != nil {
					return nil, nil, fmt.Errorf("row %d: column %q: %w", index, column.Name, err)
				}
				row[column.Name] = value
			}
		}
		renamed := make(map[string]any, len(profile.Columns))
		for _, column := range profile.Columns {
			if column.Source == "" || column.Source == column.Name || column.JSONPath != "" {
				continue
			}
			if value, ok := row[column.Source]; ok {
				renamed[column.Name] = value
			} else if _, alreadyProjected := row[column.Name]; !alreadyProjected {
				renamed[column.Name] = nil
			}
		}
		for name, value := range renamed {
			row[name] = value
		}
		// A jsonpath column's source is the root it read, not a key it consumed:
		// several columns share one JSON column, so deleting it would depend on
		// which of them happened to run first.
		for _, column := range profile.Columns {
			if column.Source == "" || column.Source == column.Name || column.JSONPath != "" {
				continue
			}
			if _, retained := outputNames[column.Source]; !retained {
				delete(row, column.Source)
			}
		}
		// Styles are evaluated last so an expression can read the projected cell
		// beside it, not just the provider's original fields.
		if styled {
			rowStyles := make(map[string]string, len(profile.Columns))
			for _, column := range profile.Columns {
				if column.Style == "" {
					continue
				}
				value, err := evalRowCEL(ctx, column.Style, row)
				if err != nil {
					return nil, nil, fmt.Errorf("row %d: column %q style: %w", index, column.Name, err)
				}
				class, ok := value.(string)
				if !ok {
					return nil, nil, fmt.Errorf("row %d: column %q style: expected a string, got %T (%v)",
						index, column.Name, value, value)
				}
				if class != "" {
					rowStyles[column.Name] = class
				}
			}
			styles = append(styles, rowStyles)
		}
		kept = append(kept, row)
	}
	return kept, styles, nil
}

func evalRowCEL(ctx context.Context, expression string, row Row) (any, error) {
	template := gomplate.Template{Expression: expression}
	for _, function := range context.CelEnvFuncs {
		template.CelEnvs = append(template.CelEnvs, function(ctx))
	}
	environment := map[string]any{"row": row, "span": row}
	for name, value := range row {
		if name != "row" && name != "span" && celIdentifier.MatchString(name) {
			environment[name] = value
		}
	}
	return gomplate.RunExpressionContext(ctx.Context, environment, template)
}

func setRowPath(row Row, path string, value any) {
	parts := strings.Split(path, ".")
	if len(parts) == 1 {
		row[path] = value
		return
	}
	current := map[string]any(row)
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

func deleteRowPath(row Row, path string) {
	parts := strings.Split(path, ".")
	if len(parts) == 1 {
		delete(row, path)
		return
	}
	current := map[string]any(row)
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			return
		}
		current = next
	}
	delete(current, parts[len(parts)-1])
}
