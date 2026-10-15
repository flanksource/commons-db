package sqlserver

import (
	"fmt"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/sqlglot"

	"github.com/flanksource/commons-db/atlas/internal/sqlx"
)

// canonical is the predicate's canonical form. Normalize sets it before the
// differ compares indexes; reaching it unset is a bug.
func (p *IndexPredicate) canonical() string {
	if p.normalized == "" {
		panic(fmt.Sprintf("sqlserver: index predicate %q compared before Normalize", p.P))
	}
	return p.normalized
}

// canonicalValue is a default expression's canonical form. SQL Server stores
// `0` as `((0))` and `getdate()` as `(getdate())`, so parentheses, case and
// brackets are normalised through sqlglot.
func canonicalValue(x string) (string, error) {
	return canonicalExpr("SELECT ", x)
}

// canonicalPredicate is a check or filter predicate's canonical form:
// `[qty]>(0)` and `qty > 0` read the same.
func canonicalPredicate(x string) (string, error) {
	return canonicalExpr("SELECT 1 WHERE ", x)
}

func canonicalExpr(prefix, x string) (string, error) {
	canonical, err := sqlglot.CanonicalSQL(prefix+x, Dialect)
	if err != nil {
		return "", fmt.Errorf("expression %q: %w", x, err)
	}
	tree, err := sqlglot.ParseOne(canonical, Dialect)
	if err != nil {
		return "", fmt.Errorf("expression %q: %w", x, err)
	}
	tree.Walk(func(e *sqlglot.Expression) bool {
		unwrapParens(e)
		return true
	})
	out, err := sqlglot.Generate(tree, Dialect)
	if err != nil {
		return "", fmt.Errorf("expression %q: %w", x, err)
	}
	return out, nil
}

// unwrapParens drops the parentheses SQL Server adds when it stores an
// expression: every pair round the whole expression (a SELECT item or a WHERE
// condition), and elsewhere only a pair round an operand that is not a binary
// operation, so `(a + b) * c` keeps the pair that gives it its meaning.
func unwrapParens(e *sqlglot.Expression) {
	root := e.Class == "Select" || e.Class == "Where"
	for key, value := range e.Args {
		switch value := value.(type) {
		case *sqlglot.Expression:
			if inner := withoutParens(value, root); inner != value {
				e.Set(key, inner)
			}
		case []*sqlglot.Expression:
			for i, child := range value {
				value[i] = withoutParens(child, root)
			}
		}
	}
}

func withoutParens(e *sqlglot.Expression, root bool) *sqlglot.Expression {
	for e != nil && e.Class == "Paren" && e.This() != nil {
		if _, binary := e.This().Args["expression"]; binary && !root {
			return e
		}
		e = e.This()
	}
	return e
}

// defaultSQL is the SQL text of a column default. An HCL string literal
// arrives unquoted and is quoted for the column's type, N'...' for the
// Unicode types; numbers and raw sql() expressions are written as they are.
func defaultSQL(c *schema.Column) (string, error) {
	switch x := c.Default.(type) {
	case *schema.RawExpr:
		return x.X, nil
	case *schema.Literal:
		return literalSQL(c, x.V)
	}
	return "", fmt.Errorf("column %q: unexpected default expression %T", c.Name, c.Default)
}

func literalSQL(c *schema.Column, v string) (string, error) {
	switch t := c.Type.Type.(type) {
	case *schema.BoolType, *schema.IntegerType, *schema.DecimalType, *schema.FloatType:
		return v, nil
	case *schema.StringType:
		unicode := t.T == "nchar" || t.T == "nvarchar" || t.T == "ntext"
		if unicode && !sqlx.IsQuoted(v, '\'') {
			quoted, err := sqlx.SingleQuote(v)
			return "N" + quoted, err
		}
	}
	return sqlx.SingleQuote(v)
}
