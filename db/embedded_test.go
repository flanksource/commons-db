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

func TestPerformanceDiagnosticStartParameters(t *testing.T) {
	assert.Empty(t, performanceDiagnosticStartParameters(false))
	assert.Equal(t, map[string]string{
		"shared_preload_libraries": "pg_stat_statements",
		"track_io_timing":          "on",
	}, performanceDiagnosticStartParameters(true))
}

func TestValidatePerformanceDiagnosticSettings(t *testing.T) {
	tests := []struct {
		name               string
		preloadedLibraries string
		trackIOTiming      string
		wantError          string
	}{
		{
			name:               "ready",
			preloadedLibraries: "auto_explain, pg_stat_statements",
			trackIOTiming:      "on",
		},
		{
			name:          "statement statistics not preloaded",
			trackIOTiming: "on",
			wantError:     "PostgreSQL performance diagnostics require shared_preload_libraries=pg_stat_statements; update the server configuration and restart PostgreSQL",
		},
		{
			name:               "IO timing disabled",
			preloadedLibraries: "pg_stat_statements",
			trackIOTiming:      "off",
			wantError:          "PostgreSQL performance diagnostics require track_io_timing=on; update the server configuration and restart PostgreSQL",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePerformanceDiagnosticSettings(tt.preloadedLibraries, tt.trackIOTiming)
			if tt.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tt.wantError)
		})
	}
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

func TestStartEmbedded_PerformanceDiagnostics(t *testing.T) {
	if os.Getenv("COMMONS_DB_EMBEDDED_TEST") == "" {
		t.Skip("set COMMONS_DB_EMBEDDED_TEST=1 to run embedded-postgres integration tests")
	}
	dir := t.TempDir()

	dsn, stop, err := StartEmbedded(EmbeddedConfig{
		DataDir:                dir,
		Database:               "diagnostics",
		PerformanceDiagnostics: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck

	var preloadedLibraries, trackIOTiming, extension string
	require.NoError(t, conn.QueryRow(ctx, "SHOW shared_preload_libraries").Scan(&preloadedLibraries))
	require.NoError(t, conn.QueryRow(ctx, "SHOW track_io_timing").Scan(&trackIOTiming))
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT extname FROM pg_extension WHERE extname = 'pg_stat_statements'").Scan(&extension))
	assert.Contains(t, preloadedLibraries, "pg_stat_statements")
	assert.Equal(t, "on", trackIOTiming)
	assert.Equal(t, "pg_stat_statements", extension)
}
