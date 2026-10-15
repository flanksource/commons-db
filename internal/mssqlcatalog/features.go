package mssqlcatalog

import (
	"context"
	"fmt"
)

// TableFeature is a table property DDL generation does not manage: Feature is
// "partitioned", or "trigger <name>" for each trigger on the table.
type TableFeature struct {
	Table   string
	Feature string
}

// UnmanagedFeatures lists the partitioned tables and table triggers in the
// scope, ordered by table then feature.
func UnmanagedFeatures(ctx context.Context, q Queryer, scope Scope) ([]TableFeature, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	var b binds
	query := fmt.Sprintf(`SELECT table_name, feature FROM (
		SELECT t.name AS table_name, CAST('partitioned' AS nvarchar(300)) AS feature
		FROM sys.tables t
		INNER JOIN sys.schemas s ON s.schema_id = t.schema_id
		INNER JOIN sys.indexes i ON i.object_id = t.object_id AND i.index_id IN (0, 1)
		INNER JOIN sys.partition_schemes ps ON ps.data_space_id = i.data_space_id
		WHERE s.name = %s%s
		UNION ALL
		SELECT t.name, CAST('trigger ' + tr.name AS nvarchar(300))
		FROM sys.triggers tr
		INNER JOIN sys.tables t ON t.object_id = tr.parent_id
		INNER JOIN sys.schemas s ON s.schema_id = t.schema_id
		WHERE s.name = %s%s
	) features
	ORDER BY table_name, feature`,
		b.add(scope.Schema), b.in("t.name", scope.Names), b.add(scope.Schema), b.in("t.name", scope.Names))
	return collect(ctx, q, query, b.args, func(rows Rows) (TableFeature, error) {
		var f TableFeature
		return f, rows.Scan(&f.Table, &f.Feature)
	})
}
