package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
)

// EmbeddedConfig configures StartEmbedded.
type EmbeddedConfig struct {
	// DataDir is the root for the data, runtime, and bin subdirectories.
	// The caller owns this choice; StartEmbedded does not pick a default path.
	DataDir string
	// Database created on first start. Defaults to "postgres" when blank.
	Database string
	// Port to bind. Zero picks a free port via FreePort().
	Port uint32
	// Username / Password. Default to "postgres"/"postgres" — fine for the
	// localhost-only instances this helper is meant for.
	Username, Password string
	// Logger receives the embedded-postgres / pg_ctl diagnostic output. Defaults
	// to os.Stderr (the library default is os.Stdout, which would corrupt callers
	// that write binary or structured data to stdout).
	Logger io.Writer
	// PerformanceDiagnostics preloads pg_stat_statements, enables I/O timing,
	// installs the extension, and fails startup if any part is unavailable.
	PerformanceDiagnostics bool
	// KeepRunning leaves a server started by this call running when stop is
	// invoked. Reused servers are always left running.
	KeepRunning bool
	// FastTesting trades durability for speed and raises the connection ceiling.
	// It is only suitable for disposable local test data.
	FastTesting bool
}

func performanceDiagnosticStartParameters(enabled bool) map[string]string {
	if !enabled {
		return nil
	}
	return map[string]string{
		"shared_preload_libraries": "pg_stat_statements",
		"track_io_timing":          "on",
	}
}

func validatePerformanceDiagnosticSettings(preloadedLibraries, trackIOTiming string) error {
	foundStatementStats := false
	for _, library := range strings.Split(preloadedLibraries, ",") {
		if strings.TrimSpace(library) == "pg_stat_statements" {
			foundStatementStats = true
			break
		}
	}
	if !foundStatementStats {
		return errors.New("PostgreSQL performance diagnostics require shared_preload_libraries=pg_stat_statements; update the server configuration and restart PostgreSQL")
	}
	if trackIOTiming != "on" {
		return errors.New("PostgreSQL performance diagnostics require track_io_timing=on; update the server configuration and restart PostgreSQL")
	}
	return nil
}

// EnsurePerformanceDiagnostics validates server-level settings before
// installing pg_stat_statements in the selected database. Server settings are
// checked first so an externally managed instance fails without partial DDL.
func EnsurePerformanceDiagnostics(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect for PostgreSQL performance diagnostics: %w", err)
	}
	defer conn.Close(context.Background()) //nolint:errcheck

	var preloadedLibraries, trackIOTiming string
	if err := conn.QueryRow(ctx, "SHOW shared_preload_libraries").Scan(&preloadedLibraries); err != nil {
		return fmt.Errorf("read shared_preload_libraries: %w", err)
	}
	if err := conn.QueryRow(ctx, "SHOW track_io_timing").Scan(&trackIOTiming); err != nil {
		return fmt.Errorf("read track_io_timing: %w", err)
	}
	if err := validatePerformanceDiagnosticSettings(preloadedLibraries, trackIOTiming); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pg_stat_statements"); err != nil {
		return fmt.Errorf("install pg_stat_statements: %w", err)
	}
	return nil
}

// FreePort binds :0 to discover a free TCP port. Public so callers can reuse
// it for adjacent services (e.g. postgrest) that need an unclaimed port.
func FreePort() (int, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, fmt.Errorf("listen :0: %w", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// detectPGVersion reads data/PG_VERSION to pick the embedded-postgres version
// that matches the on-disk cluster. It uses V16 only for a fresh data directory.
func detectPGVersion(dataPath string) (embeddedpostgres.PostgresVersion, error) {
	raw, err := os.ReadFile(filepath.Join(dataPath, "PG_VERSION"))
	if os.IsNotExist(err) {
		return embeddedpostgres.V16, nil
	}
	if err != nil {
		return "", fmt.Errorf("read PostgreSQL data version from %s: %w", dataPath, err)
	}
	switch strings.TrimSpace(string(raw)) {
	case "9.6":
		return embeddedpostgres.V9, nil
	case "10":
		return embeddedpostgres.V10, nil
	case "11":
		return embeddedpostgres.V11, nil
	case "12":
		return embeddedpostgres.V12, nil
	case "13":
		return embeddedpostgres.V13, nil
	case "14":
		return embeddedpostgres.V14, nil
	case "15":
		return embeddedpostgres.V15, nil
	case "16":
		return embeddedpostgres.V16, nil
	case "17":
		return embeddedpostgres.V17, nil
	case "18":
		return embeddedpostgres.V18, nil
	default:
		return "", fmt.Errorf(
			"unsupported PostgreSQL data version %q in %s",
			strings.TrimSpace(string(raw)),
			filepath.Join(dataPath, "PG_VERSION"),
		)
	}
}

// postmasterLinePort is the line index in postmaster.pid that holds the
// listening port. Format is stable across postgres releases.
const postmasterLinePort = 3

func readPostmasterPort(dataPath string) (uint32, bool) {
	pidPath := filepath.Join(dataPath, "postmaster.pid")
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, false
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) <= postmasterLinePort {
		return 0, false
	}
	p, err := strconv.ParseUint(strings.TrimSpace(lines[postmasterLinePort]), 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(p), true
}

// waitReady polls the DSN with pgx.Connect until it responds or timeout
// elapses. The fergusstrange library's Start() claims readiness, but we've
// seen races where the first query after Start still fails with "the
// database system is starting up" — so we double-check here.
func waitReady(dsn string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		conn, err := pgx.Connect(ctx, dsn)
		cancel()
		if err == nil {
			_ = conn.Close(context.Background())
			return nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("timeout")
	}
	return lastErr
}
