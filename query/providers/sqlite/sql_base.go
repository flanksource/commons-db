package sqlite

import (
	"fmt"
	"strings"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// sqlBaseCTE is the name the author's statement is wrapped under. A collision
// is an error rather than a rename, because a query that silently referred to
// someone else's CTE would be a different query.
const sqlBaseCTE = "__cdb_base"

// sqlQuery is the author's statement and what its param markers stand for:
// Args in marker order, and the identifier paths its identifier markers name.
type sqlQuery struct {
	Text        string
	Args        []any
	Identifiers []string
}

// parseBase reads the author's statement in the statement's dialect and binds
// its params, so they number before anything the wrapper binds.
//
// A statement the dialect's parser cannot read fails here, naming the dialect:
// there is no text path to fall back to, because the wrapper only ever writes
// trees.
func (s *sqlStatement) parseBase(query sqlQuery) (sqlNode, error) {
	glot, err := s.dialect.glot()
	if err != nil {
		return nil, err
	}
	// clickhouse-go fills every @name in the text, literals included, so a
	// query spelling one of the wrapper's own names would have it filled.
	if s.dialect == dialectClickHouse && strings.Contains(query.Text, "@"+clickHouseBindPrefix) {
		return nil, fmt.Errorf("the query contains %q, which ClickHouse column filter values are bound by; rename it",
			"@"+clickHouseBindPrefix)
	}
	tree, err := sqlglot.ParseOne(query.Text, glot)
	if err != nil {
		return nil, fmt.Errorf("the query cannot be read as %s SQL, which column filters and paging need to wrap it: %w",
			glot, err)
	}
	// Drivers number placeholders across the whole statement, so the args the
	// wrapper is about to supply would bind to the author's marker instead of
	// to the wrapper's — and a query that today fails cleanly with "there is no
	// parameter $1" would start returning confidently wrong rows.
	if len(tree.FindAll("Placeholder", "Parameter")) > 0 {
		return nil, fmt.Errorf(
			"the query already contains a bind placeholder, which a column filter's own values would be numbered against; inline the value or remove the placeholder to filter this query")
	}
	if err := materializeIdentifiers(tree, query.Identifiers); err != nil {
		return nil, err
	}
	if err := s.materializeParams(tree, query.Args); err != nil {
		return nil, err
	}
	return tree, nil
}

// wrapAsBaseCTE makes base the __cdb_base CTE of outer, hoisting any CTEs the
// author already wrote into the same list rather than nesting them.
//
// Nesting is not an option: SQL Server rejects a WITH inside a CTE body
// outright. Hoisting is legal in every dialect, so there is one shape to build
// and one shape to test.
func wrapAsBaseCTE(base, outer sqlNode) error {
	with, _ := base.Args["with_"].(sqlNode)
	if with == nil {
		with = sqlglot.New("With")
	} else {
		base.Set("with_", nil)
	}
	existing, _ := with.Args["expressions"].([]sqlNode)
	for _, cte := range existing {
		alias, _ := cte.Args["alias"].(sqlNode)
		if strings.EqualFold(alias.Name(), sqlBaseCTE) {
			return fmt.Errorf(
				"the query already defines a CTE named %q, which is the name column filters wrap it in; rename it", sqlBaseCTE)
		}
	}
	with.Append("expressions", sqlglot.New("CTE", kv("this", base),
		kv("alias", sqlglot.New("TableAlias", kv("this", identifier(sqlBaseCTE))))))
	outer.Set("with_", with)
	return nil
}
