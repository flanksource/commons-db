package sqlite

import (
	"fmt"
	"strconv"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// Every statement this provider runs is a sqlglot tree written out by the
// connection dialect's generator, never text assembled around the author's
// SQL: an identifier is an Identifier node the generator quotes, and a value is
// a bind node whose value travels as an argument. These are the node shapes the
// builders share, copied from what sqlglot's own parser builds for the same SQL.

type sqlNode = *sqlglot.Expression

func kv(key string, value any) sqlglot.Arg { return sqlglot.Arg{Key: key, Value: value} }

func sqlNodes(n ...sqlNode) []sqlNode { return n }

// identifier is a quoted name, so the generator escapes whatever it holds.
func identifier(name string) sqlNode {
	return sqlglot.New("Identifier", kv("this", name), kv("quoted", true))
}

// bareIdentifier is an unquoted name; only the provider's own result aliases
// (value, count, total) are written bare, as they always have been.
func bareIdentifier(name string) sqlNode {
	return sqlglot.New("Identifier", kv("this", name), kv("quoted", false))
}

func columnNode(name string) sqlNode { return sqlglot.New("Column", kv("this", identifier(name))) }

// allColumnsOf is `"table".*`.
func allColumnsOf(table string) sqlNode {
	return sqlglot.New("Column", kv("this", sqlglot.New("Star")), kv("table", identifier(table)))
}

// baseTable is the wrapped author's statement as a FROM item.
func baseTable() sqlNode { return sqlglot.New("Table", kv("this", identifier(sqlBaseCTE))) }

func number(n int) sqlNode {
	return sqlglot.New("Literal", kv("this", strconv.Itoa(n)), kv("is_string", false))
}

func stringLiteral(text string) sqlNode {
	return sqlglot.New("Literal", kv("this", text), kv("is_string", true))
}

func paren(n sqlNode) sqlNode { return sqlglot.New("Paren", kv("this", n)) }

func negate(n sqlNode) sqlNode { return sqlglot.New("Not", kv("this", n)) }

func binary(class string, left, right sqlNode) sqlNode {
	return sqlglot.New(class, kv("this", left), kv("expression", right))
}

// isNull and isNotNull test a copy of n, so one column node can be compared
// in several places of a tree.
func isNull(n sqlNode) sqlNode { return binary("Is", n.Copy(), sqlglot.New("Null")) }

func isNotNull(n sqlNode) sqlNode { return negate(isNull(n)) }

func aliased(n sqlNode, name sqlNode) sqlNode {
	return sqlglot.New("Alias", kv("this", n), kv("alias", name))
}

func inList(n sqlNode, values []sqlNode) sqlNode {
	return sqlglot.New("In", kv("this", n), kv("expressions", values))
}

// conjunction joins terms with class (And or Or). The generator writes a
// connector without deciding precedence, so a group of more than one term is
// parenthesised here — the same grouping squirrel used to write — and a group
// of one is the term itself. Nil terms are skipped; no terms is nil.
func conjunction(class string, terms []sqlNode) sqlNode {
	var joined sqlNode
	count := 0
	for _, term := range terms {
		if term == nil {
			continue
		}
		count++
		if joined == nil {
			joined = term
			continue
		}
		joined = binary(class, joined, term)
	}
	if count > 1 {
		return paren(joined)
	}
	return joined
}

// countOf is COUNT(this). T-SQL writes the reference's big-integer count as
// COUNT_BIG, so it alone takes the plain one.
func countOf(dialect sqlDialect, this sqlNode) sqlNode {
	return sqlglot.New("Count", kv("this", this), kv("big_int", dialect != dialectSQLServer))
}

func countDistinct(dialect sqlDialect, this sqlNode) sqlNode {
	return countOf(dialect, sqlglot.New("Distinct", kv("expressions", sqlNodes(this))))
}

// totalOver is COUNT(*) OVER (), the size of the whole set on every row.
func totalOver(dialect sqlDialect) sqlNode {
	return sqlglot.New("Window", kv("this", countOf(dialect, sqlglot.New("Star"))), kv("over", "OVER"))
}

func fromItem(item sqlNode) sqlNode { return sqlglot.New("From", kv("this", item)) }

func whereOf(condition sqlNode) sqlNode {
	if condition == nil {
		return nil
	}
	return sqlglot.New("Where", kv("this", condition))
}

func subqueryAs(query sqlNode, alias string) sqlNode {
	return sqlglot.New("Subquery", kv("this", query),
		kv("alias", sqlglot.New("TableAlias", kv("this", identifier(alias)))))
}

// replaceNode puts replacement where old sits in its parent.
func replaceNode(old, replacement sqlNode) error {
	parent := old.Parent
	if parent == nil {
		return fmt.Errorf("cannot replace the root %s node", old.Class)
	}
	for _, key := range parent.Keys {
		switch value := parent.Args[key].(type) {
		case *sqlglot.Expression:
			if value == old {
				parent.Set(key, replacement)
				return nil
			}
		case []*sqlglot.Expression:
			for index, child := range value {
				if child == old {
					children := append([]*sqlglot.Expression(nil), value...)
					children[index] = replacement
					parent.Set(key, children)
					return nil
				}
			}
		}
	}
	return fmt.Errorf("%s node is not a child of its parent %s", old.Class, parent.Class)
}
