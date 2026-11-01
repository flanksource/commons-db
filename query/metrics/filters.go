package metrics

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Matcher is one label predicate pushed down to a metric source, in
// Prometheus matcher terms: Op is "=", "!=", "=~" or "!~". Pushdown only
// narrows what a source fetches; SQLite still evaluates the full WHERE clause,
// so a source may ignore any matcher it cannot express.
type Matcher struct {
	Label string
	Op    string
	Value string
}

// operand is one right-hand value of a predicate: a string literal, or the
// index of a bound metric SQL parameter.
type operand struct {
	text  string
	param int
	bound bool
}

// predicate is a WHERE conjunct over one label column, before its bound
// parameters are known. op is the SQL operator: =, !=, in, not in, like,
// not like.
type predicate struct {
	label, op string
	values    []operand
}

func sourcePredicates(scope node, alias string) []predicate {
	where, _ := scope.Args["where"].(node)
	if where == nil {
		return nil
	}
	condition, _ := where.Args["this"].(node)
	var result []predicate
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
		if p, ok := predicateOf(n, scope, alias); ok {
			result = append(result, p)
		}
	}
	visit(condition)
	return result
}

func predicateOf(n node, scope node, alias string) (predicate, bool) {
	negated := false
	if n.Class == "Not" {
		negated = true
		n, _ = n.Args["this"].(node)
		if n == nil || (n.Class != "In" && n.Class != "Like") {
			return predicate{}, false
		}
	}
	// sqlglot flags NOT LIKE on the Like node itself, while NOT IN wraps it.
	if n.Args["negate"] == true {
		if negated || n.Class != "Like" {
			return predicate{}, false
		}
		negated = true
	}
	ops := map[string]string{"EQ": "=", "NEQ": "!=", "In": "in", "Like": "like"}
	op, ok := ops[n.Class]
	if !ok {
		return predicate{}, false
	}
	left, _ := n.Args["this"].(node)
	if !isLabelColumn(left, scope, alias) {
		return predicate{}, false
	}
	var right []node
	if n.Class == "In" {
		right, _ = n.Args["expressions"].([]node)
	} else if r, _ := n.Args["expression"].(node); r != nil {
		right = []node{r}
	}
	if len(right) == 0 {
		return predicate{}, false
	}
	p := predicate{label: left.Name(), op: op}
	if negated {
		p.op = "not " + op
	}
	for _, r := range right {
		value, ok := operandOf(r)
		if !ok {
			return predicate{}, false
		}
		p.values = append(p.values, value)
	}
	return p, true
}

func isLabelColumn(n node, scope node, alias string) bool {
	if n == nil || n.Class != "Column" {
		return false
	}
	qualifier, _ := n.Args["table"].(node)
	if qualifier != nil {
		return qualifier.Name() == alias
	}
	return len(scope.FindAll("Table")) == 1
}

func operandOf(n node) (operand, bool) {
	if n.Class == "Literal" && n.Args["is_string"] == true {
		return operand{text: n.Name()}, true
	}
	if n.Class == "Placeholder" && strings.HasPrefix(n.Name(), "metric_param_") {
		index, err := strconv.Atoi(strings.TrimPrefix(n.Name(), "metric_param_"))
		return operand{param: index, bound: true}, err == nil
	}
	return operand{}, false
}

// resolveMatchers binds parameters into predicates. A predicate over a non
// string parameter is not pushed: SQLite compares it with column affinity and
// its text form here could differ, so only SQLite may decide it.
func resolveMatchers(predicates []predicate, args []any) ([]Matcher, error) {
	matchers := []Matcher{}
	for _, p := range predicates {
		values, pushable, err := operandValues(p.values, args)
		if err != nil {
			return nil, err
		}
		if pushable {
			matchers = append(matchers, matcherOf(p.label, p.op, values))
		}
	}
	return matchers, nil
}

func operandValues(operands []operand, args []any) ([]string, bool, error) {
	values := make([]string, 0, len(operands))
	for _, o := range operands {
		if !o.bound {
			values = append(values, o.text)
			continue
		}
		if o.param < 0 || o.param >= len(args) {
			return nil, false, fmt.Errorf("missing metric SQL parameter %d", o.param)
		}
		text, ok := args[o.param].(string)
		if !ok {
			return nil, false, nil
		}
		values = append(values, text)
	}
	return values, true, nil
}

func matcherOf(label, op string, values []string) Matcher {
	switch op {
	case "=", "!=":
		return Matcher{Label: label, Op: op, Value: values[0]}
	case "in", "not in":
		quoted := make([]string, len(values))
		for i, v := range values {
			quoted[i] = regexp.QuoteMeta(v)
		}
		return Matcher{Label: label, Op: regexOp(op), Value: "^(" + strings.Join(quoted, "|") + ")$"}
	default:
		// SQLite LIKE folds ASCII case, so the regex folds case too: a case
		// sensitive pattern would drop series SQLite would have kept.
		return Matcher{Label: label, Op: regexOp(op), Value: "(?i)^" + likePattern(values[0]) + "$"}
	}
}

func regexOp(op string) string {
	if strings.HasPrefix(op, "not ") {
		return "!~"
	}
	return "=~"
}

func likePattern(like string) string {
	var b strings.Builder
	for _, r := range like {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}
