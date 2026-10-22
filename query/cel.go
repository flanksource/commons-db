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
	plan, err := newRowTransformPlan(profile)
	if err != nil {
		return nil, nil, err
	}
	kept := rows[:0]
	var styles []map[string]string
	for index, row := range rows {
		keep, rowStyles, err := plan.transformRow(ctx, row)
		if err != nil {
			return nil, nil, fmt.Errorf("row %d: %w", index, err)
		}
		if !keep {
			continue
		}
		if plan.styled {
			styles = append(styles, rowStyles)
		}
		kept = append(kept, row)
	}
	return kept, styles, nil
}

// rowTransformPlan is what applyRowTransforms compiles once from a profile and
// then applies to every row.
type rowTransformPlan struct {
	profile     Profile
	outputNames map[string]struct{}
	jsonPaths   map[string]jp.Expr
	filters     []FilterDef
	styled      bool
}

func newRowTransformPlan(profile Profile) (rowTransformPlan, error) {
	plan := rowTransformPlan{
		profile:     profile,
		outputNames: make(map[string]struct{}, len(profile.Columns)),
		jsonPaths:   make(map[string]jp.Expr, len(profile.Columns)),
	}
	for _, column := range profile.Columns {
		plan.outputNames[column.Name] = struct{}{}
	}
	for _, column := range profile.Columns {
		if column.JSONPath == "" {
			continue
		}
		expression, err := compileColumnJSONPath(column)
		if err != nil {
			return rowTransformPlan{}, err
		}
		plan.jsonPaths[column.Name] = expression
	}
	filters, err := activeFilters(profile.Filters)
	if err != nil {
		return rowTransformPlan{}, err
	}
	plan.filters = filters
	for _, column := range profile.Columns {
		if column.Style != "" {
			plan.styled = true
			break
		}
	}
	return plan, nil
}

// transformRow mutates row in place and reports whether it survives the
// filters, with its cell styles when the profile declares any.
func (plan rowTransformPlan) transformRow(ctx context.Context, row Row) (bool, map[string]string, error) {
	if err := validateAcyclicValue(row); err != nil {
		return false, nil, fmt.Errorf("provider row: %w", err)
	}
	if err := plan.applyAliases(ctx, row); err != nil {
		return false, nil, err
	}
	if len(plan.filters) > 0 {
		keep, err := keepRow(ctx, plan.filters, row)
		if err != nil || !keep {
			return false, nil, err
		}
	}
	if err := plan.projectColumns(ctx, row); err != nil {
		return false, nil, err
	}
	plan.renameSources(row)
	// Styles are evaluated last so an expression can read the projected cell
	// beside it, not just the provider's original fields.
	if !plan.styled {
		return true, nil, nil
	}
	rowStyles, err := plan.rowStyles(ctx, row)
	return err == nil, rowStyles, err
}

func (plan rowTransformPlan) applyAliases(ctx context.Context, row Row) error {
	profile := plan.profile
	type aliasValue struct {
		name  string
		value any
	}
	projected := make([]aliasValue, 0, len(profile.Aliases))
	ignoredNames := make(map[string]struct{}, len(profile.Ignore))
	for _, alias := range profile.Aliases {
		if alias.Name == "" || alias.CEL == "" {
			return fmt.Errorf("alias name and cel are required")
		}
		value, err := evalRowCEL(ctx, alias.CEL, row)
		if err != nil {
			return fmt.Errorf("alias %q: %w", alias.Name, err)
		}
		if err := setRowPath(row, alias.Name, value); err != nil {
			return fmt.Errorf("alias %q: %w", alias.Name, err)
		}
		projected = append(projected, aliasValue{name: alias.Name, value: value})
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
		if err := setRowPath(row, alias.name, alias.value); err != nil {
			return fmt.Errorf("alias %q: %w", alias.name, err)
		}
	}
	return nil
}

func (plan rowTransformPlan) projectColumns(ctx context.Context, row Row) error {
	for _, column := range plan.profile.Columns {
		var value any
		var err error
		switch {
		case column.CEL != "":
			value, err = evalRowCEL(ctx, column.CEL, row)
		case column.JSONPath != "":
			value, err = evalRowJSONPath(plan.jsonPaths[column.Name], column.Source, row)
		default:
			continue
		}
		if err != nil {
			return fmt.Errorf("column %q: %w", column.Name, err)
		}
		if err := setRowValue(row, column.Name, value); err != nil {
			return fmt.Errorf("column %q: %w", column.Name, err)
		}
	}
	return nil
}

func (plan rowTransformPlan) renameSources(row Row) {
	columns := plan.profile.Columns
	renamed := make(map[string]any, len(columns))
	for _, column := range columns {
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
	for _, column := range columns {
		if column.Source == "" || column.Source == column.Name || column.JSONPath != "" {
			continue
		}
		if _, retained := plan.outputNames[column.Source]; !retained {
			delete(row, column.Source)
		}
	}
}

func (plan rowTransformPlan) rowStyles(ctx context.Context, row Row) (map[string]string, error) {
	rowStyles := make(map[string]string, len(plan.profile.Columns))
	for _, column := range plan.profile.Columns {
		if column.Style == "" {
			continue
		}
		value, err := evalRowCEL(ctx, column.Style, row)
		if err != nil {
			return nil, fmt.Errorf("column %q style: %w", column.Name, err)
		}
		class, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("column %q style: expected a string, got %T", column.Name, value)
		}
		if class != "" {
			rowStyles[column.Name] = class
		}
	}
	return rowStyles, nil
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

func setRowValue(row Row, name string, value any) error {
	if err := validateContainerValue(map[string]any(row), value); err != nil {
		return err
	}
	row[name] = value
	return nil
}

func setRowPath(row Row, path string, value any) error {
	parts := strings.Split(path, ".")
	if len(parts) == 1 {
		return setRowValue(row, path, value)
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
	if err := validateContainerValue(current, value); err != nil {
		return err
	}
	current[parts[len(parts)-1]] = value
	return nil
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
