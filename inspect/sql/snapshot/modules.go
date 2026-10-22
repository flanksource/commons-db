package snapshot

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
)

func readCollations(ctx context.Context, db Querier, c *catalog) error {
	return visit(ctx, db, schemaQuery{SQL: `SELECT o.name, c.name, c.collation_name
		FROM sys.columns c JOIN sys.objects o ON o.object_id=c.object_id
		JOIN sys.schemas s ON s.schema_id=o.schema_id
		WHERE s.name=@p1 AND o.type IN ('U','V') AND c.collation_name IS NOT NULL
		ORDER BY o.name, c.column_id`, Schema: c.schema}, func(rows *sql.Rows) error {
		var table, column, collation string
		if err := rows.Scan(&table, &column, &collation); err != nil {
			return err
		}
		c.collations[[2]string{table, column}] = collation
		return nil
	})
}

func readTriggers(ctx context.Context, db Querier, c *catalog) error {
	return visit(ctx, db, schemaQuery{SQL: `SELECT SCHEMA_NAME(trigger_object.schema_id), tr.name, s.name, o.name, tr.is_disabled, m.definition
		FROM sys.triggers tr JOIN sys.objects o ON o.object_id=tr.parent_id
		JOIN sys.objects trigger_object ON trigger_object.object_id=tr.object_id
		JOIN sys.schemas s ON s.schema_id=o.schema_id
		LEFT JOIN sys.sql_modules m ON m.object_id=tr.object_id
		WHERE s.name=@p1 AND tr.parent_class=1 ORDER BY tr.name`, Schema: c.schema}, func(rows *sql.Rows) error {
		var d migrate.Definition
		var parent migrate.Name
		var definition sql.NullString
		if err := rows.Scan(&d.Name.Schema, &d.Name.Name, &parent.Schema, &parent.Name, &d.Disabled, &definition); err != nil {
			return err
		}
		if !definition.Valid || definition.String == "" {
			return fmt.Errorf("trigger %v has no readable definition", d.Name)
		}
		d.Kind, d.SQL, d.Parent = "TRIGGER", definition.String, &parent
		c.modules = append(c.modules, d)
		return nil
	})
}

func readDependencies(ctx context.Context, db Querier, c *catalog) error {
	return visit(ctx, db, schemaQuery{SQL: `SELECT DISTINCT o.name, d.referenced_server_name, d.referenced_database_name,
		COALESCE(SCHEMA_NAME(r.schema_id), d.referenced_schema_name), d.referenced_entity_name, d.referenced_id
		FROM sys.sql_expression_dependencies d JOIN sys.objects o ON o.object_id=d.referencing_id
		JOIN sys.schemas s ON s.schema_id=o.schema_id LEFT JOIN sys.objects r ON r.object_id=d.referenced_id
		WHERE s.name=@p1 AND d.referenced_class=1 AND o.type IN ('V','P','FN','IF','TF','TR')
		ORDER BY 1, 4, 5`, Schema: c.schema}, func(rows *sql.Rows) error {
		var owner, entity string
		var server, database, schema sql.NullString
		var id sql.NullInt64
		if err := rows.Scan(&owner, &server, &database, &schema, &entity, &id); err != nil {
			return err
		}
		if server.Valid || database.Valid || !schema.Valid || !id.Valid {
			return fmt.Errorf("module %s.%s has an external or unresolved dependency %q", c.schema, owner, entity)
		}
		name := migrate.Name{Schema: schema.String, Name: entity}
		if !slices.Contains(c.dependencies[owner], name) {
			c.dependencies[owner] = append(c.dependencies[owner], name)
		}
		return nil
	})
}

type schemaQuery struct {
	SQL, Schema string
}

func visit(ctx context.Context, db Querier, query schemaQuery, scan func(*sql.Rows) error) (err error) {
	rows, err := db.QueryContext(ctx, query.SQL, query.Schema)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
	}()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
