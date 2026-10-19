package sqlite

import (
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

// The slots of an array template: names a template is written with where a
// node built from the request goes once the template is parsed.
const (
	sqlArraySlot  = "__cdb_slot_array"
	sqlColumnSlot = "__cdb_slot_column"
	sqlValuesSlot = "__cdb_slot_values"
)

// sqlArrayShape is how one dialect expands a JSON array into one row per
// member, as SQL fragments in that dialect: from expands the array in the
// __cdb_slot_array slot under the alias __cdb_item, value is a member's text,
// member holds for a string member, and join is how a lookup joins the
// expansion to the rows it came from.
//
// The fragments are constants written here and parsed into a tree, never text
// joined to a request: what the request supplies — the column, the values —
// replaces a slot as a node after parsing. The provider's own names are
// written bare and quoted structurally (see quoteProviderNames).
type sqlArrayShape struct {
	from, value, member, join string
}

var sqlArrayShapes = map[sqlDialect]sqlArrayShape{
	dialectSQLite: {
		from:   "json_each(" + sqlArraySlot + ") AS __cdb_item",
		value:  "__cdb_item.value",
		member: "__cdb_item.type = 'text'",
		join:   "CROSS JOIN",
	},
	dialectPostgres: {
		from: "jsonb_array_elements(COALESCE(NULLIF(to_jsonb(" + sqlArraySlot +
			"), 'null'::jsonb), '[]'::jsonb)) AS __cdb_item(__cdb_value)",
		value:  "(__cdb_item.__cdb_value #>> '{}')",
		member: "jsonb_typeof(__cdb_item.__cdb_value) = 'string'",
		join:   "CROSS JOIN LATERAL",
	},
	dialectMySQL: {
		from: "JSON_TABLE(IF(JSON_TYPE(" + sqlArraySlot + ") = 'ARRAY', " + sqlArraySlot +
			", JSON_ARRAY()), '$[*]' COLUMNS (__cdb_value JSON PATH '$')) AS __cdb_item",
		value:  "JSON_UNQUOTE(__cdb_item.__cdb_value)",
		member: "JSON_TYPE(__cdb_item.__cdb_value) = 'STRING'",
		join:   "CROSS JOIN",
	},
	dialectSQLServer: {
		from:   "OPENJSON(COALESCE(CONVERT(nvarchar(max), " + sqlArraySlot + "), N'[]')) AS __cdb_item",
		value:  "__cdb_item.[value]",
		member: "__cdb_item.[type] = 1",
		join:   "CROSS APPLY",
	},
}

func sqlArrayShapeFor(dialect sqlDialect) (sqlArrayShape, error) {
	shape, ok := sqlArrayShapes[dialect]
	if !ok {
		return sqlArrayShape{}, fmt.Errorf("the %s dialect has no array expansion", dialect)
	}
	return shape, nil
}

// parseTemplate parses one of the provider's constant SQL templates in the
// dialect and quotes the provider's own names in it.
func parseTemplate(dialect sqlDialect, template string) (sqlNode, error) {
	glot, err := dialect.glot()
	if err != nil {
		return nil, err
	}
	tree, err := sqlglot.ParseOne(template, glot)
	if err != nil {
		return nil, fmt.Errorf("the %s array template does not parse: %w", glot, err)
	}
	quoteProviderNames(tree)
	return tree, nil
}

// quoteProviderNames quotes every __cdb_ name, which the templates write bare
// so one spelling serves every dialect's quote characters.
func quoteProviderNames(tree sqlNode) {
	for _, name := range tree.FindAll("Identifier") {
		if strings.HasPrefix(name.Name(), "__cdb_") {
			name.Set("quoted", true)
		}
	}
}

// fillSlot replaces every column named slot with a fresh node from fill.
func fillSlot(tree sqlNode, slot string, fill func() sqlNode) error {
	for _, column := range tree.FindAll("Column") {
		if column.Name() != slot || column.Args["table"] != nil {
			continue
		}
		if err := replaceNode(column, fill()); err != nil {
			return err
		}
	}
	return nil
}

// sqlArrayPredicate keeps a row whose array holds any included string, and
// drops one whose array holds any excluded string. SQL NULL and an empty array
// hold no values, so exclusion keeps both. JSON backends deliberately ignore
// non-string members because filter values have a string identity.
func (s *sqlStatement) sqlArrayPredicate(column sqlNode, filter query.ColumnFilterValue) (sqlNode, error) {
	if kind := filter.Kind.CompilesAs(); kind != query.ColumnFilterKindTerms {
		return nil, fmt.Errorf("field %q is an array, which only a value selection compares; got a %s filter",
			filter.Field, filter.Kind.Normalized())
	}
	if s.dialect == dialectClickHouse {
		return s.clickHouseArrayPredicate(column, filter), nil
	}
	shape, err := sqlArrayShapeFor(s.dialect)
	if err != nil {
		return nil, fmt.Errorf("field %q is an array: %w", filter.Field, err)
	}
	conditions := []sqlNode{}
	for _, side := range []struct {
		values  []string
		exclude bool
	}{{filter.Include, false}, {filter.Exclude, true}} {
		if len(side.values) == 0 {
			continue
		}
		members, err := s.arrayMembers(shape, column, side.values)
		if err != nil {
			return nil, err
		}
		exists := sqlglot.New("Exists", kv("this", members))
		if side.exclude {
			exists = negate(exists)
		}
		conditions = append(conditions, exists)
	}
	return conjunction("And", conditions), nil
}

// arrayMembers is `SELECT 1 FROM <expansion> WHERE (<string member> AND
// <member> IN (values))`, the members of column's array among values.
func (s *sqlStatement) arrayMembers(shape sqlArrayShape, column sqlNode, values []string) (sqlNode, error) {
	members, err := parseTemplate(s.dialect, "SELECT 1 FROM "+shape.from+
		" WHERE ("+shape.member+" AND "+shape.value+" IN ("+sqlValuesSlot+"))")
	if err != nil {
		return nil, err
	}
	if err := fillSlot(members, sqlArraySlot, column.Copy); err != nil {
		return nil, err
	}
	for _, list := range members.FindAll("In") {
		bound := make([]any, len(values))
		for index, value := range values {
			bound[index] = value
		}
		list.Set("expressions", s.bindAll(bound))
	}
	return members, nil
}

func (s *sqlStatement) clickHouseArrayPredicate(column sqlNode, filter query.ColumnFilterValue) sqlNode {
	included := []sqlNode{}
	for _, value := range filter.Include {
		included = append(included, binary("ArrayContains", column.Copy(), s.bind(value)))
	}
	conditions := sqlNodes(conjunction("Or", included))
	for _, value := range filter.Exclude {
		conditions = append(conditions, negate(binary("ArrayContains", column.Copy(), s.bind(value))))
	}
	return conjunction("And", conditions)
}

// arrayLookupSelect is the lookup over an array column: each member a value,
// counted once per row that holds it — however many times that row's array
// repeats it. Rows are numbered first, so COUNT(DISTINCT) can tell them apart.
// It returns the select, the member value its predicates compare, and the
// string-member test (nil on ClickHouse, whose arrays are typed).
func arrayLookupSelect(dialect sqlDialect, column sqlNode) (sqlNode, sqlNode, sqlNode, error) {
	template := "SELECT __cdb_item AS value, COUNT(*) AS count, COUNT(*) OVER () AS total" +
		" FROM (SELECT " + sqlColumnSlot + " AS __cdb_array FROM " + sqlBaseCTE + ") AS __cdb_rows" +
		" ARRAY JOIN arrayDistinct(__cdb_rows.__cdb_array) AS __cdb_item GROUP BY __cdb_item"
	if dialect != dialectClickHouse {
		shape, err := sqlArrayShapeFor(dialect)
		if err != nil {
			return nil, nil, nil, err
		}
		template = "SELECT " + shape.value + " AS value, COUNT(DISTINCT __cdb_rows.__cdb_row) AS count, COUNT(*) OVER () AS total" +
			" FROM (SELECT ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) AS __cdb_row, " + sqlColumnSlot +
			" AS __cdb_array FROM " + sqlBaseCTE + ") AS __cdb_rows " + shape.join + " " + shape.from +
			" WHERE " + shape.member + " GROUP BY " + shape.value
	}
	selection, err := parseTemplate(dialect, template)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := fillSlot(selection, sqlColumnSlot, column.Copy); err != nil {
		return nil, nil, nil, err
	}
	rowsArray := func() sqlNode {
		return sqlglot.New("Column", kv("this", identifier("__cdb_array")), kv("table", identifier("__cdb_rows")))
	}
	if err := fillSlot(selection, sqlArraySlot, rowsArray); err != nil {
		return nil, nil, nil, err
	}
	expressions, _ := selection.Args["expressions"].([]sqlNode)
	value := expressions[0].This()
	var member sqlNode
	if where, _ := selection.Args["where"].(sqlNode); where != nil {
		member = where.This()
	}
	return selection, value, member, nil
}
