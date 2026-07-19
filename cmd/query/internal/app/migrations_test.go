package app

import (
	"context"
	"testing"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/stretchr/testify/require"
)

func TestMigrateSchemaAndProfileStore(t *testing.T) {
	// A fresh, un-migrated database is the premise of this test: it fabricates a
	// pre-HCL schema and asserts the migrator upgrades it in place.
	handle := dbtest.ForT(t, dbtest.Options{Name: "query_migrate", LogName: "query-migrate-test"})
	dsn, gdb := handle.DSN(), handle.Gorm()

	pgulidSQL, err := querySchema.ReadFile("migrations/001_generate_ulid.sql")
	require.NoError(t, err)
	require.NoError(t, gdb.Exec(string(pgulidSQL)).Error)
	require.NoError(t, gdb.AutoMigrate(&models.Connection{}), "create the pre-HCL GORM schema")
	require.NoError(t, gdb.Create(&models.Connection{Name: "existing", Type: models.ConnectionTypePostgres}).Error)
	require.NoError(t, gdb.Exec("CREATE TABLE migration_api_unmanaged (id integer PRIMARY KEY)").Error)
	require.NoError(t, migrateSchema(t.Context(), dsn))
	require.NoError(t, migrateSchema(t.Context(), dsn), "migration must be idempotent")

	for _, table := range []string{"connections", "profiles", "properties", "migration_api_unmanaged"} {
		var exists bool
		require.NoError(t, gdb.Raw(`SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = ?
		)`, table).Scan(&exists).Error)
		require.True(t, exists, "%s should exist", table)
	}
	var existing models.Connection
	require.NoError(t, gdb.Where("name = ?", "existing").First(&existing).Error)
	require.Equal(t, models.ConnectionTypePostgres, existing.Type)
	require.NoError(t, gdb.Create(&models.AppProperty{Name: "query.page-size", Value: "100"}).Error)
	var property models.AppProperty
	require.NoError(t, gdb.First(&property, "name = ?", "query.page-size").Error)
	require.Equal(t, "100", property.Value)

	dir := t.TempDir()
	files, err := profiles.NewFileStore(dir)
	require.NoError(t, err)
	require.NoError(t, files.Save(context.Background(), query.Profile{
		Name: "Imported", Provider: query.ProviderConfig{Type: "sql", Connection: "connection://db"},
		Query: "select * from a where region = '{{.params.region}}'",
	}))
	databaseProfiles, err := profiles.NewDBStore(gdb)
	require.NoError(t, err)
	require.NoError(t, profiles.Import(context.Background(), files, databaseProfiles))

	got, err := databaseProfiles.Get(context.Background(), "Imported")
	require.NoError(t, err)
	require.Equal(t, "select * from a where region = '{{.params.region}}'", got.Query)

	got.Query = "select 2"
	require.NoError(t, databaseProfiles.Save(context.Background(), got))
	fresh, err := profiles.NewDBStore(gdb)
	require.NoError(t, err)
	got, err = fresh.Get(context.Background(), "Imported")
	require.NoError(t, err)
	require.Equal(t, "select 2", got.Query)
}
