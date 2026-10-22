package snapshot

import (
	"context"
	"fmt"

	"github.com/flanksource/commons-db/internal/mssqlcatalog"
	"github.com/flanksource/sqlglot-go/migrate"
)

func read(ctx context.Context, db Querier, name string) (catalog, error) {
	c := catalog{schema: name, collations: map[[2]string]string{}, dependencies: map[string][]migrate.Name{}}
	q, scope := mssqlcatalog.FromDB(db), mssqlcatalog.Scope{Schema: name}
	found, err := mssqlcatalog.Schemas(ctx, q, []string{name})
	if err != nil {
		return c, err
	}
	if len(found) != 1 {
		return c, fmt.Errorf("schema %q was not found", name)
	}
	steps := []func() error{
		func() error { var err error; c.tables, err = mssqlcatalog.Tables(ctx, q, scope, ""); return err },
		func() error { var err error; c.columns, err = mssqlcatalog.Columns(ctx, q, scope); return err },
		func() error { var err error; c.indexes, err = mssqlcatalog.Indexes(ctx, q, scope); return err },
		func() error { var err error; c.parts, err = mssqlcatalog.IndexColumns(ctx, q, scope); return err },
		func() error { var err error; c.keys, err = mssqlcatalog.ForeignKeys(ctx, q, scope); return err },
		func() error { var err error; c.checks, err = mssqlcatalog.CheckConstraints(ctx, q, scope); return err },
		func() error { var err error; c.routines, err = mssqlcatalog.Routines(ctx, q, scope); return err },
		func() error { return readCollations(ctx, db, &c) },
		func() error { return readTriggers(ctx, db, &c) },
		func() error { return readDependencies(ctx, db, &c) },
		func() error { return refuseUnsupported(ctx, db, name) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return c, err
		}
	}
	return c, nil
}
