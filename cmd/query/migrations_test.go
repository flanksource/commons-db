package main

import (
	"os"
	"path/filepath"
	"testing"

	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/models"
	"github.com/stretchr/testify/require"
)

func TestMigrateSchemaAndProfileStore(t *testing.T) {
	if os.Getenv("COMMONS_DB_EMBEDDED_TEST") == "" {
		t.Skip("set COMMONS_DB_EMBEDDED_TEST=1 to run embedded-postgres integration tests")
	}
	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "query_migrate",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })

	gdb, pool, err := commonsdb.SetupDB(dsn, "query-migrate-test")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	pgulidSQL, err := querySchema.ReadFile("migrations/001_generate_ulid.sql")
	require.NoError(t, err)
	require.NoError(t, gdb.Exec(string(pgulidSQL)).Error)
	require.NoError(t, gdb.AutoMigrate(&models.Connection{}), "create the pre-HCL GORM schema")
	require.NoError(t, gdb.Create(&models.Connection{Name: "existing", Type: models.ConnectionTypePostgres}).Error)
	require.NoError(t, gdb.Exec("CREATE TABLE migration_api_unmanaged (id integer PRIMARY KEY)").Error)
	require.NoError(t, migrateSchema(t.Context(), dsn))
	require.NoError(t, migrateSchema(t.Context(), dsn), "migration must be idempotent")

	for _, table := range []string{"connections", "profiles", "migration_api_unmanaged"} {
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

	dir := t.TempDir()
	files, err := NewProfileStore(dir)
	require.NoError(t, err)
	require.NoError(t, files.Save(sampleProfile("Imported")))
	require.NoError(t, files.UseDB(gdb))

	got, err := files.Get("Imported")
	require.NoError(t, err)
	require.Equal(t, "select * from a where region = '{{.params.region}}'", got.Query)

	got.Query = "select 2"
	require.NoError(t, files.Save(got))
	fresh, err := NewProfileStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, fresh.UseDB(gdb))
	got, err = fresh.Get("Imported")
	require.NoError(t, err)
	require.Equal(t, "select 2", got.Query)
}
