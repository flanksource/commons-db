package metrics

import (
	"strconv"
	"strings"
)

func sourceFilters(scope node, alias string) (map[string]string, map[string]int) {
	result := map[string]string{}
	bound := map[string]int{}
	where, _ := scope.Args["where"].(node)
	if where == nil {
		return result, bound
	}
	condition, _ := where.Args["this"].(node)
	var visit func(node)
	visit = func(n node) {
		if n == nil {
			return
		}
		if n.Class == "And" || n.Class == "Paren" {
			left, _ := n.Args["this"].(node)
			right, _ := n.Args["expression"].(node)
			visit(left)
			visit(right)
			return
		}
		if n.Class != "EQ" {
			return
		}
		left, _ := n.Args["this"].(node)
		right, _ := n.Args["expression"].(node)
		if left == nil || right == nil || left.Class != "Column" {
			return
		}
		qualifier, _ := left.Args["table"].(node)
		if qualifier != nil && qualifier.Name() != alias {
			return
		}
		if qualifier == nil && len(scope.FindAll("Table")) != 1 {
			return
		}
		if right.Class == "Literal" && right.Args["is_string"] == true {
			result[left.Name()] = right.Name()
		}
		if right.Class == "Placeholder" && strings.HasPrefix(right.Name(), "metric_param_") {
			if index, err := strconv.Atoi(strings.TrimPrefix(right.Name(), "metric_param_")); err == nil {
				bound[left.Name()] = index
			}
		}
	}
	visit(condition)
	return result, bound
}
