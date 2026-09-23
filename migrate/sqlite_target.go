package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/schema"
	atlassqlite "ariga.io/atlas/sql/sqlite"
	_ "modernc.org/sqlite"

	sqlitemigrate "github.com/flanksource/commons-db/migrate/sqlite"
)

func applySQLite(ctx context.Context, connection string, schemaFS fs.FS, config options) error {
	if schemaFS == nil {
		return errors.New("schema filesystem is nil")
	}
	if config.schema != defaultSchema {
		return fmt.Errorf("SQLite migrations do not support schema %q", config.schema)
	}
	if len(config.exclude) > 0 {
		return errors.New("SQLite migrations do not support inspection exclusions")
	}
	if config.allowDrops {
		return errors.New("SQLite migrations do not support destructive changes")
	}
	scripts, err := loadScripts(schemaFS, config.dir)
	if err != nil {
		return err
	}
	if len(scripts) > 0 {
		return errors.New("SQLite migrations do not support SQL migration files")
	}
	parser, security, err := loadHCL(schemaFS, config.dir, config.input)
	if err != nil {
		return err
	}
	if len(security.Roles) > 0 || len(security.Permissions) > 0 {
		return errors.New("SQLite migrations do not support roles or permissions")
	}
	desired := &schema.Realm{}
	if err := postgres.EvalHCL.Eval(parser, desired, config.input); err != nil {
		return fmt.Errorf("evaluate HCL schemas for SQLite: %w", err)
	}
	tables, err := projectSQLiteRealm(desired)
	if err != nil {
		return fmt.Errorf("project HCL schema to SQLite: %w", err)
	}
	return reconcileSQLite(ctx, connection, tables, config.allowRebuilds)
}

func reconcileSQLite(ctx context.Context, connection string, tables []*schema.Table, allowRebuilds bool) (returnErr error) {
	database, err := sql.Open("sqlite", connection)
	if err != nil {
		return fmt.Errorf("open SQLite migration database: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, database.Close()) }()
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("connect SQLite migration database: %w", err)
	}
	tx, err := atlassqlite.OpenTx(ctx, database, nil)
	if err != nil {
		return fmt.Errorf("begin SQLite migration: %w", err)
	}
	if err := sqlitemigrate.ReconcileTables(ctx, tx, sqlitemigrate.ReconcileOptions{AllowRebuilds: allowRebuilds}, tables...); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return errors.Join(fmt.Errorf("commit SQLite migration: %w", err), tx.Rollback())
	}
	return nil
}

func projectSQLiteRealm(realm *schema.Realm) ([]*schema.Table, error) {
	if realm == nil {
		return nil, errors.New("desired realm is nil")
	}
	if len(realm.Schemas) != 1 {
		return nil, fmt.Errorf("expected one schema, got %d", len(realm.Schemas))
	}
	if len(realm.Attrs) > 0 || len(realm.Objects) > 0 {
		return nil, errors.New("realm attributes and objects are not portable to SQLite")
	}
	scope := realm.Schemas[0]
	if scope.Name != defaultSchema {
		return nil, fmt.Errorf("canonical HCL must declare schema %q, got %q", defaultSchema, scope.Name)
	}
	if len(scope.Views) > 0 || len(scope.Funcs) > 0 || len(scope.Procs) > 0 || len(scope.Objects) > 0 || len(scope.Attrs) > 0 {
		return nil, errors.New("schema views, functions, procedures, attributes, and objects are not portable to SQLite")
	}
	scope.Name = "main"
	for _, table := range scope.Tables {
		if err := projectSQLiteTable(table, scope); err != nil {
			return nil, err
		}
	}
	return scope.Tables, nil
}

func projectSQLiteTable(table *schema.Table, scope *schema.Schema) error {
	if table == nil {
		return errors.New("canonical HCL contains a nil table")
	}
	if len(table.Triggers) > 0 {
		return fmt.Errorf("table %q declares triggers, which are not portable to SQLite", table.Name)
	}
	table.Schema = scope
	for _, column := range table.Columns {
		jsonColumn, err := projectSQLiteColumn(table.Name, column)
		if err != nil {
			return err
		}
		if jsonColumn {
			if err := addSQLiteJSONCheck(table, column.Name); err != nil {
				return err
			}
		}
	}
	if table.PrimaryKey != nil {
		if err := projectSQLiteIndex(table.Name, table.PrimaryKey); err != nil {
			return err
		}
	}
	for _, index := range table.Indexes {
		if err := projectSQLiteIndex(table.Name, index); err != nil {
			return err
		}
	}
	for _, foreignKey := range table.ForeignKeys {
		if len(foreignKey.Attrs) > 0 {
			return fmt.Errorf("table %q foreign key %q has PostgreSQL-only attributes", table.Name, foreignKey.Symbol)
		}
	}
	for _, attr := range table.Attrs {
		check, ok := attr.(*schema.Check)
		if !ok {
			return fmt.Errorf("table %q has non-portable attribute %T", table.Name, attr)
		}
		if len(check.Attrs) > 0 {
			return fmt.Errorf("table %q check %q has non-portable attributes", table.Name, check.Name)
		}
	}
	return nil
}

func projectSQLiteColumn(table string, column *schema.Column) (bool, error) {
	if column == nil || column.Type == nil || column.Type.Type == nil {
		return false, fmt.Errorf("table %q contains a column without a type", table)
	}
	if len(column.Attrs) > 0 {
		return false, fmt.Errorf("table %q column %q has non-portable attributes", table, column.Name)
	}
	switch column.Type.Type.(type) {
	case *schema.UUIDType:
		column.Type.Type, column.Type.Raw = &schema.StringType{T: "text"}, "text"
	case *schema.JSONType:
		column.Type.Type, column.Type.Raw = &schema.StringType{T: "text"}, "text"
		return true, nil
	case *schema.TimeType:
		column.Type.Type, column.Type.Raw = &schema.TimeType{T: "datetime"}, "datetime"
	case *schema.IntegerType:
		column.Type.Type, column.Type.Raw = &schema.IntegerType{T: "integer"}, "integer"
	case *schema.StringType:
		column.Type.Type, column.Type.Raw = &schema.StringType{T: "text"}, "text"
	case *schema.BoolType:
		column.Type.Raw = "bool"
	case *schema.BinaryType:
		column.Type.Type, column.Type.Raw = &schema.BinaryType{T: "blob"}, "blob"
	case *schema.DecimalType:
		column.Type.Type, column.Type.Raw = &schema.DecimalType{T: "numeric"}, "numeric"
	case *schema.FloatType:
		column.Type.Type, column.Type.Raw = &schema.FloatType{T: "real"}, "real"
	default:
		return false, fmt.Errorf("table %q column %q type %T is not portable to SQLite", table, column.Name, column.Type.Type)
	}
	return false, nil
}

func projectSQLiteIndex(table string, index *schema.Index) error {
	if index == nil {
		return fmt.Errorf("table %q contains a nil index", table)
	}
	attrs := make([]schema.Attr, 0, len(index.Attrs))
	for _, attr := range index.Attrs {
		switch attr := attr.(type) {
		case *postgres.Constraint:
			if !attr.IsUnique() {
				return fmt.Errorf("table %q index %q has non-portable constraint", table, index.Name)
			}
		case *postgres.IndexPredicate:
			attrs = append(attrs, &atlassqlite.IndexPredicate{P: attr.P})
		case *postgres.IndexType:
			if attr.T != "" && !strings.EqualFold(attr.T, "btree") {
				return fmt.Errorf("table %q index %q type %q is not portable to SQLite", table, index.Name, attr.T)
			}
		default:
			return fmt.Errorf("table %q index %q has non-portable attribute %T", table, index.Name, attr)
		}
	}
	for _, part := range index.Parts {
		if part == nil || part.C == nil || part.X != nil {
			return fmt.Errorf("table %q index %q contains a non-portable expression", table, index.Name)
		}
		if len(part.Attrs) > 0 {
			return fmt.Errorf("table %q index %q has non-portable column attributes", table, index.Name)
		}
	}
	index.Attrs = attrs
	return nil
}

func addSQLiteJSONCheck(table *schema.Table, column string) error {
	name := table.Name + "_" + column + "_json"
	quoted := `"` + strings.ReplaceAll(column, `"`, `""`) + `"`
	expression := "json_valid(" + quoted + ")"
	for _, attr := range table.Attrs {
		if check, ok := attr.(*schema.Check); ok && check.Name == name {
			if check.Expr != expression || len(check.Attrs) > 0 {
				return fmt.Errorf("table %q check %q conflicts with generated SQLite JSON check", table.Name, name)
			}
			return nil
		}
	}
	table.Attrs = append(table.Attrs, &schema.Check{Name: name, Expr: expression})
	return nil
}
