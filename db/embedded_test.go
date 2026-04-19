package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFreePort_ReturnsPositive is cheap and runs without spinning postgres.
func TestFreePort_ReturnsPositive(t *testing.T) {
	port, err := FreePort()
	require.NoError(t, err)
	assert.Greater(t, port, 0)
	assert.Less(t, port, 65536)
}

// TestStartEmbedded_StartPingStop is an integration test. The
// fergusstrange/embedded-postgres library downloads a ~50MB postgres tarball
// on first run — in CI environments without network we skip rather than fail.
// Set COMMONS_DB_EMBEDDED_TEST=1 to opt in locally.
func TestStartEmbedded_StartPingStop(t *testing.T) {
	if os.Getenv("COMMONS_DB_EMBEDDED_TEST") == "" {
		t.Skip("set COMMONS_DB_EMBEDDED_TEST=1 to run embedded-postgres integration tests")
	}
	dir := t.TempDir()

	dsn, stop, err := StartEmbedded(EmbeddedConfig{
		DataDir:  dir,
		Database: "test",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck

	var one int
	require.NoError(t, conn.QueryRow(context.Background(), "select 1").Scan(&one))
	assert.Equal(t, 1, one)

	// Cluster files should have been created under the DataDir.
	_, statErr := os.Stat(filepath.Join(dir, "data", "PG_VERSION"))
	assert.NoError(t, statErr, "embedded postgres should have initialized a cluster")
}
