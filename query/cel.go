package query

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/flanksource/gomplate/v3"

	"github.com/flanksource/commons-db/context"
)

var celIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func applyRowTransforms(ctx context.Context, profile Profile, rows []Row) error {
	for index, row := range rows {
		aliasRoots := map[string]bool{}
		for _, alias := range profile.Aliases {
			if alias.Name == "" || alias.CEL == "" {
				return fmt.Errorf("row %d: alias name and cel are required", index)
			}
			value, err := evalRowCEL(ctx, alias.CEL, row)
			if err != nil {
				return fmt.Errorf("row %d: alias %q: %w", index, alias.Name, err)
			}
			setRowPath(row, alias.Name, value)
			if strings.Contains(alias.Name, ".") {
				aliasRoots[strings.Split(alias.Name, ".")[0]] = true
			}
		}
		for _, ignored := range profile.Ignore {
			if aliasRoots[ignored] {
				continue
			}
			delete(row, ignored)
			deleteRowPath(row, ignored)
		}
		for _, column := range profile.Columns {
			if column.CEL == "" {
				continue
			}
			value, err := evalRowCEL(ctx, column.CEL, row)
			if err != nil {
				return fmt.Errorf("row %d: column %q: %w", index, column.Name, err)
			}
			row[column.Name] = value
		}
	}
	return nil
}

func applyColumns(ctx context.Context, columns []ColumnDef, rows []Row) error {
	return applyRowTransforms(ctx, Profile{Columns: columns}, rows)
}

func evalRowCEL(ctx context.Context, expression string, row Row) (any, error) {
	template := gomplate.Template{Expression: expression}
	for _, function := range context.CelEnvFuncs {
		template.CelEnvs = append(template.CelEnvs, function(ctx))
	}
	environment := map[string]any{"row": row, "span": row}
	for name, value := range row {
		if celIdentifier.MatchString(name) {
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
