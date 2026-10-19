package xetrace

import (
	"fmt"
	"strconv"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// The Extended Events DDL is built as a sqlglot tree and written by its T-SQL
// generator, never assembled as text: a session name is an Identifier and a
// path or filter value a National string, so quoting and escaping are the
// generator's job alone.

type ddlNode = *sqlglot.Expression

func kv(key string, value any) sqlglot.Arg { return sqlglot.Arg{Key: key, Value: value} }

func ddlNodes(n ...ddlNode) []ddlNode { return n }

func ident(name string) ddlNode {
	return sqlglot.New("Identifier", kv("this", name), kv("quoted", false))
}

// sessionIdent is the session's name, always bracket-quoted.
func sessionIdent(name string) ddlNode {
	return sqlglot.New("Identifier", kv("this", name), kv("quoted", true))
}

// xeName is a package-qualified XE object: an event, action, target, or the
// global field a predicate reads, e.g. sqlserver.database_name.
type xeName struct{ pkg, name string }

func (n xeName) column() ddlNode { return qualifiedColumn(n.pkg, n.name) }

func qualifiedColumn(table, name string) ddlNode {
	return sqlglot.New("Column", kv("this", ident(name)), kv("table", ident(table)))
}

// eventField is one of an event's own, unqualified fields, e.g. duration.
func eventField(name string) ddlNode { return sqlglot.New("Column", kv("this", ident(name))) }

func number[T int | int64](n T) ddlNode {
	return sqlglot.New("Literal", kv("this", strconv.FormatInt(int64(n), 10)), kv("is_string", false))
}

func national(s string) ddlNode { return sqlglot.New("National", kv("this", s)) }

func word(w string) ddlNode { return sqlglot.New("Var", kv("this", w)) }

func paren(n ddlNode) ddlNode { return sqlglot.New("Paren", kv("this", n)) }

func negate(n ddlNode) ddlNode { return sqlglot.New("Not", kv("this", n)) }

func binary(class string, left, right ddlNode) ddlNode {
	return sqlglot.New(class, kv("this", left), kv("expression", right))
}

// joinConditions folds conditions left to right with class (And or Or), or
// returns nil when there are none.
func joinConditions(class string, conditions []ddlNode) ddlNode {
	var out ddlNode
	for _, c := range conditions {
		if out == nil {
			out = c
			continue
		}
		out = binary(class, out, c)
	}
	return out
}

// xeCall is a predicate source function of the sqlserver package, e.g.
// sqlserver.equal_i_sql_unicode_string(sqlserver.database_name, N'value').
func xeCall(fn string, args ...ddlNode) ddlNode {
	return sqlglot.New("Dot",
		kv("this", ident("sqlserver")),
		kv("expression", sqlglot.New("Anonymous", kv("this", fn), kv("expressions", args))))
}

// option is one `name = value` of an event's SET, a target's SET or the
// session's WITH list; unit is "", "SECONDS", "KB" or "MB".
func option(name string, value ddlNode, unit string) ddlNode {
	args := []sqlglot.Arg{kv("this", word(name)), kv("value", value)}
	if unit != "" {
		args = append(args, kv("unit", unit))
	}
	return sqlglot.New("EventSessionOption", args...)
}

func generateTSQL(tree ddlNode) (string, error) {
	sql, err := sqlglot.Generate(tree, "tsql")
	if err != nil {
		return "", fmt.Errorf("write %s: %w", tree.Class, err)
	}
	return sql, nil
}
