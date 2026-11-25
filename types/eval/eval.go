// Package eval evaluates the CEL expressions and Go templates carried by the
// types package. It is separate from types so that importing the type
// definitions does not link the gomplate template engine.
package eval

import (
	"fmt"

	"github.com/flanksource/gomplate/v4"

	"github.com/flanksource/commons-db/types"
)

// Cel evaluates a CEL expression against env.
func Cel(env map[string]any, expr types.CelExpression) (string, error) {
	return gomplate.RunTemplate(env, gomplate.Template{Expression: string(expr)})
}

// Template renders a Go template against env.
func Template(env map[string]any, tmpl types.GoTemplate) (string, error) {
	return gomplate.RunTemplate(env, gomplate.Template{Template: string(tmpl)})
}

// Value returns the static value when set, otherwise the evaluated expression.
func Value(env map[string]any, value types.ValueExpression) (string, error) {
	if value.Value != "" {
		return value.Value, nil
	}
	return Cel(env, value.Expr)
}

// ResourceSelector hydrates an EnvVarResourceSelector into a ResourceSelector,
// evaluating every non-empty field against env.
func ResourceSelector(env map[string]any, selector types.EnvVarResourceSelector) (*types.ResourceSelector, error) {
	rs := types.ResourceSelector{Scope: selector.Scope, Cache: selector.Cache}

	if err := evalFields(env, []field{
		{name: "agent", expr: selector.Agent, target: &rs.Agent},
		{name: "id", expr: selector.ID, target: &rs.ID},
		{name: "name", expr: selector.Name, target: &rs.Name},
		{name: "namespace", expr: selector.Namespace, target: &rs.Namespace},
		{name: "tagSelector", expr: selector.TagSelector, target: &rs.TagSelector},
		{name: "labelSelector", expr: selector.LabelSelector, target: &rs.LabelSelector},
		{name: "fieldSelector", expr: selector.FieldSelector, target: &rs.FieldSelector},
	}); err != nil {
		return nil, err
	}

	var err error
	if rs.Types, err = evalList(env, "type", selector.Types); err != nil {
		return nil, err
	}
	if rs.Statuses, err = evalList(env, "status", selector.Statuses); err != nil {
		return nil, err
	}
	for i, expr := range selector.Healths {
		if expr.Empty() {
			continue
		}
		health, err := Value(env, expr)
		if err != nil {
			return nil, fmt.Errorf("failed to evaluate health at index %d: %w", i, err)
		}
		rs.Health.Add(health)
	}
	return &rs, nil
}

type field struct {
	name   string
	expr   types.ValueExpression
	target *string
}

func evalFields(env map[string]any, fields []field) error {
	for _, f := range fields {
		if f.expr.Empty() {
			continue
		}
		value, err := Value(env, f.expr)
		if err != nil {
			return fmt.Errorf("failed to evaluate %s: %w", f.name, err)
		}
		*f.target = value
	}
	return nil
}

func evalList(env map[string]any, kind string, exprs []types.ValueExpression) ([]string, error) {
	if len(exprs) == 0 {
		return nil, nil
	}
	values := make([]string, len(exprs))
	for i, expr := range exprs {
		if expr.Empty() {
			continue
		}
		value, err := Value(env, expr)
		if err != nil {
			return nil, fmt.Errorf("failed to evaluate %s at index %d: %w", kind, i, err)
		}
		values[i] = value
	}
	return values, nil
}
