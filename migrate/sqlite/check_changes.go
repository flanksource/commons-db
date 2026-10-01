package sqlite

import (
	"strings"

	"ariga.io/atlas/sql/schema"
)

// Atlas pairs named checks without comparing their expressions, so add the missing modification.
func withCheckExpressionChanges(current, desired *schema.Schema, changes []schema.Change) []schema.Change {
	for _, to := range desired.Tables {
		from, found := current.Table(to.Name)
		if !found {
			continue
		}
		known := map[string]*schema.Check{}
		for _, attr := range from.Attrs {
			if check, ok := attr.(*schema.Check); ok && check.Name != "" {
				known[check.Name] = check
			}
		}
		var modified []schema.Change
		for _, attr := range to.Attrs {
			check, ok := attr.(*schema.Check)
			if !ok || check.Name == "" {
				continue
			}
			if existing := known[check.Name]; existing != nil && normalizedCheckExpression(existing.Expr) != normalizedCheckExpression(check.Expr) {
				modified = append(modified, &schema.ModifyCheck{From: existing, To: check})
			}
		}
		if len(modified) == 0 {
			continue
		}
		merged := false
		for _, change := range changes {
			if table, ok := change.(*schema.ModifyTable); ok && table.T.Name == to.Name {
				table.Changes = append(table.Changes, modified...)
				merged = true
				break
			}
		}
		if !merged {
			changes = append(changes, &schema.ModifyTable{T: to, Changes: modified})
		}
	}
	return changes
}

func normalizedCheckExpression(expression string) string {
	expression = strings.TrimSpace(expression)
	for len(expression) > 1 && expression[0] == '(' && expression[len(expression)-1] == ')' && wrappingParentheses(expression) {
		expression = strings.TrimSpace(expression[1 : len(expression)-1])
	}
	return expression
}

func wrappingParentheses(expression string) bool {
	depth := 0
	var quote byte
	for i := 0; i < len(expression); i++ {
		switch {
		case quote != 0:
			if expression[i] == quote {
				if i+1 < len(expression) && expression[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			}
		case expression[i] == '\'' || expression[i] == '"' || expression[i] == '`':
			quote = expression[i]
		case expression[i] == '[':
			quote = ']'
		case expression[i] == '(':
			depth++
		case expression[i] == ')':
			depth--
			if depth == 0 && i < len(expression)-1 {
				return false
			}
		}
	}
	return depth == 0 && quote == 0
}
