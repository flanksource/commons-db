package mssqlcatalog

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Index is one named index on a table. KeyColumns is the ordered key (included
// columns excluded); IndexColumns returns the full per-column detail. TypeDesc
// is sys.indexes.type_desc (CLUSTERED, NONCLUSTERED, ...); Filter is the
// filtered-index predicate, empty when unfiltered.
type Index struct {
	Table            string
	Name             string
	Unique           bool
	Primary          bool
	UniqueConstraint bool
	KeyColumns       []string
	TypeDesc         string
	Filter           string
}

// Indexes lists every named index on every table in the scope, ordered by
// table then index name. Views are absent: the join is to sys.tables.
//
// Key columns are aggregated with FOR XML PATH, not STRING_AGG ... WITHIN
// GROUP, which compatibility levels below 140 reject. The `, TYPE).value(...)`
// projection keeps XML metacharacters intact and yields nvarchar(max).
func Indexes(ctx context.Context, q Queryer, scope Scope) ([]Index, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	var b binds
	query := fmt.Sprintf(`SELECT
		t.name AS table_name,
		i.name AS index_name,
		i.is_unique,
		i.is_primary_key,
		STUFF((
			SELECT ',' + c.name
			FROM sys.index_columns ic2
			INNER JOIN sys.columns c ON ic2.object_id = c.object_id AND ic2.column_id = c.column_id
			WHERE ic2.object_id = i.object_id AND ic2.index_id = i.index_id AND ic2.key_ordinal > 0
			ORDER BY ic2.key_ordinal
			FOR XML PATH(''), TYPE
		).value('.', 'nvarchar(max)'), 1, 1, '') AS columns,
		i.type_desc,
		COALESCE(i.filter_definition, ''),
		i.is_unique_constraint
	FROM sys.indexes i
	INNER JOIN sys.tables t ON i.object_id = t.object_id
	INNER JOIN sys.schemas s ON t.schema_id = s.schema_id
	WHERE s.name = %s AND i.name IS NOT NULL%s
	ORDER BY t.name, i.name`, b.add(scope.Schema), b.in("t.name", scope.Names))
	return collect(ctx, q, query, b.args, func(rows Rows) (Index, error) {
		var idx Index
		var columns sql.NullString
		// Bit columns decode to Go bools in go-mssqldb; scan them as bools.
		err := rows.Scan(&idx.Table, &idx.Name, &idx.Unique, &idx.Primary, &columns, &idx.TypeDesc, &idx.Filter, &idx.UniqueConstraint)
		if columns.Valid && columns.String != "" {
			idx.KeyColumns = strings.Split(columns.String, ",")
		}
		return idx, err
	})
}

// IndexColumn is one column of a named index. KeyOrdinal is 0 for included
// columns.
type IndexColumn struct {
	Table      string
	Index      string
	Column     string
	KeyOrdinal int
	Included   bool
	Descending bool
}

// IndexColumns lists the key and included columns of every named index in the
// scope, ordered by table, index, key columns by key ordinal, then included
// columns by index column id.
func IndexColumns(ctx context.Context, q Queryer, scope Scope) ([]IndexColumn, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	var b binds
	query := fmt.Sprintf(`SELECT
		t.name, i.name, c.name, ic.key_ordinal, ic.is_included_column, ic.is_descending_key
	FROM sys.index_columns ic
	INNER JOIN sys.indexes i ON i.object_id = ic.object_id AND i.index_id = ic.index_id
	INNER JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
	INNER JOIN sys.tables t ON t.object_id = i.object_id
	INNER JOIN sys.schemas s ON s.schema_id = t.schema_id
	WHERE s.name = %s AND i.name IS NOT NULL%s
	ORDER BY t.name, i.name, ic.is_included_column, ic.key_ordinal, ic.index_column_id`,
		b.add(scope.Schema), b.in("t.name", scope.Names))
	return collect(ctx, q, query, b.args, func(rows Rows) (IndexColumn, error) {
		var col IndexColumn
		return col, rows.Scan(&col.Table, &col.Index, &col.Column, &col.KeyOrdinal, &col.Included, &col.Descending)
	})
}

// ForeignKey is one participating column of a foreign key. OnDelete and
// OnUpdate are the referential action descriptions (NO_ACTION, CASCADE, ...).
type ForeignKey struct {
	Table     string
	Name      string
	Column    string
	RefSchema string
	RefTable  string
	RefColumn string
	OnDelete  string
	OnUpdate  string
}

// ForeignKeys lists every foreign key column in the scope. Rows are ordered by
// table, constraint, then constraint_column_id; that last key is load-bearing,
// because callers pair local and referenced columns positionally.
func ForeignKeys(ctx context.Context, q Queryer, scope Scope) ([]ForeignKey, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	var b binds
	query := fmt.Sprintf(`SELECT
			t1.name AS table_name,
			fk.name AS constraint_name,
			c1.name AS column_name,
			s2.name AS ref_schema_name,
			t2.name AS ref_table_name,
			c2.name AS ref_column_name,
			fk.delete_referential_action_desc,
			fk.update_referential_action_desc
		FROM sys.foreign_keys fk
		INNER JOIN sys.foreign_key_columns fkc ON fk.object_id = fkc.constraint_object_id
		INNER JOIN sys.tables t1 ON fk.parent_object_id = t1.object_id
		INNER JOIN sys.schemas s1 ON t1.schema_id = s1.schema_id
		INNER JOIN sys.columns c1 ON fkc.parent_object_id = c1.object_id AND fkc.parent_column_id = c1.column_id
		INNER JOIN sys.tables t2 ON fk.referenced_object_id = t2.object_id
		INNER JOIN sys.schemas s2 ON t2.schema_id = s2.schema_id
		INNER JOIN sys.columns c2 ON fkc.referenced_object_id = c2.object_id AND fkc.referenced_column_id = c2.column_id
		WHERE s1.name = %s%s
		ORDER BY t1.name, fk.name, fkc.constraint_column_id`, b.add(scope.Schema), b.in("t1.name", scope.Names))
	return collect(ctx, q, query, b.args, func(rows Rows) (ForeignKey, error) {
		var fk ForeignKey
		return fk, rows.Scan(&fk.Table, &fk.Name, &fk.Column, &fk.RefSchema, &fk.RefTable, &fk.RefColumn, &fk.OnDelete, &fk.OnUpdate)
	})
}

// CheckConstraint is one CHECK constraint. Column is set only for a
// column-level constraint.
type CheckConstraint struct {
	Table      string
	Name       string
	Definition string
	Column     *string
	Disabled   bool
}

// CheckConstraints lists every check constraint in the scope ordered by table
// then constraint name.
func CheckConstraints(ctx context.Context, q Queryer, scope Scope) ([]CheckConstraint, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	var b binds
	query := fmt.Sprintf(`SELECT
		t.name, cc.name, cc.definition, c.name, cc.is_disabled
	FROM sys.check_constraints cc
	INNER JOIN sys.tables t ON t.object_id = cc.parent_object_id
	INNER JOIN sys.schemas s ON s.schema_id = t.schema_id
	LEFT JOIN sys.columns c ON c.object_id = cc.parent_object_id AND c.column_id = cc.parent_column_id
	WHERE s.name = %s%s
	ORDER BY t.name, cc.name`, b.add(scope.Schema), b.in("t.name", scope.Names))
	return collect(ctx, q, query, b.args, func(rows Rows) (CheckConstraint, error) {
		var check CheckConstraint
		var column sql.NullString
		err := rows.Scan(&check.Table, &check.Name, &check.Definition, &column, &check.Disabled)
		check.Column = nullableString(column)
		return check, err
	})
}
