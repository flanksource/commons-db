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
	"github.com/flanksource/commons/logger"
	"github.com/jackc/pgx/v5"
)

// EmbeddedConfig configures StartEmbedded.
type EmbeddedConfig struct {
	// DataDir is where postgres keeps its cluster, runtime, and binaries.
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
}

// StartEmbedded launches a fergusstrange/embedded-postgres under cfg.DataDir
// and returns the DSN plus a stop() closer. If a postmaster.pid exists in the
// data directory we assume a previous instance is still up and reuse its
// port (reading posmasterLinePort from postmaster.pid), matching the
// reference implementation in duty/start.go:205.
//
// The returned stop() is a no-op when we reused an existing postmaster — we
// don't own that process, so we must not stop it.
func StartEmbedded(cfg EmbeddedConfig) (dsn string, stop func() error, err error) {
	if cfg.DataDir == "" {
		return "", nil, errors.New("EmbeddedConfig.DataDir is required")
	}
	if cfg.Database == "" {
		cfg.Database = "postgres"
	}
	if cfg.Username == "" {
		cfg.Username = "postgres"
	}
	if cfg.Password == "" {
		cfg.Password = "postgres"
	}
	port := cfg.Port
	if port == 0 {
		p, err := FreePort()
		if err != nil {
			return "", nil, fmt.Errorf("pick free port: %w", err)
		}
		port = uint32(p) //nolint:gosec // p from net listener is always < 65536
	}

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return "", nil, fmt.Errorf("create data dir %s: %w", cfg.DataDir, err)
	}
	if err := os.Chmod(cfg.DataDir, 0o750); err != nil {
		logger.V(1).Infof("chmod %s: %v", cfg.DataDir, err)
	}

	dataPath := filepath.Join(cfg.DataDir, "data")
	if err := os.MkdirAll(dataPath, 0o750); err != nil {
		return "", nil, fmt.Errorf("create data dir %s: %w", dataPath, err)
	}

	pgVersion := detectPGVersion(dataPath)

	logger.Infof("Starting embedded postgres at %s (version %s, port %d)", cfg.DataDir, pgVersion, port)

	// Logger defaults to stderr (the library default is stdout, which corrupts
	// callers that emit binary/structured data on stdout, e.g. a CLI piping a PDF).
	pgLog := cfg.Logger
	if pgLog == nil {
		pgLog = os.Stderr
	}
	serverConfig := embeddedpostgres.DefaultConfig().
		Port(port).
		DataPath(dataPath).
		RuntimePath(filepath.Join(cfg.DataDir, "runtime")).
		BinariesPath(filepath.Join(cfg.DataDir, "bin")).
		Version(pgVersion).
		Username(cfg.Username).Password(cfg.Password).
		Database(cfg.Database).
		Logger(pgLog)
	if parameters := performanceDiagnosticStartParameters(cfg.PerformanceDiagnostics); len(parameters) > 0 {
		serverConfig = serverConfig.StartParameters(parameters)
	}
	server := embeddedpostgres.NewDatabase(serverConfig)

	ownedByUs := true
	if err := server.Start(); err != nil {
		if reusedPort, ok := detectPostmasterPort(cfg.DataDir, err); ok {
			// Existing postmaster is still up. Reuse its port and treat the
			// stop() closure as a no-op so we don't terminate someone else's
			// process.
			port = reusedPort
			ownedByUs = false
			logger.Infof("reusing existing embedded postgres on port %d", port)
		} else {
			return "", nil, fmt.Errorf("start embedded postgres: %w", err)
		}
	}

	stop = func() error {
		if !ownedByUs {
			return nil
		}
		return server.Stop()
	}

	dsn = fmt.Sprintf("postgres://%s:%s@localhost:%d/%s?sslmode=disable",
		cfg.Username, cfg.Password, port, cfg.Database)

	if err := waitReady(dsn, 10*time.Second); err != nil {
		_ = stop()
		return "", nil, fmt.Errorf("embedded postgres never became ready: %w", err)
	}
	if cfg.PerformanceDiagnostics {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := EnsurePerformanceDiagnostics(ctx, dsn); err != nil {
			_ = stop()
			return "", nil, err
		}
	}
	return dsn, stop, nil
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
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// detectPGVersion reads data/PG_VERSION to pick the embedded-postgres version
// that matches the on-disk cluster. Falls back to V16 for a fresh data dir.
func detectPGVersion(dataPath string) embeddedpostgres.PostgresVersion {
	raw, err := os.ReadFile(filepath.Join(dataPath, "PG_VERSION"))
	if err != nil {
		return embeddedpostgres.V16
	}
	switch strings.TrimSpace(string(raw)) {
	case "14":
		return embeddedpostgres.V14
	case "15":
		return embeddedpostgres.V15
	case "16":
		return embeddedpostgres.V16
	default:
		return embeddedpostgres.V16
	}
}

// posmasterLinePort is the line index in postmaster.pid that holds the
// listening port. Format is stable across postgres releases.
const posmasterLinePort = 3

// detectPostmasterPort parses data/postmaster.pid when server.Start fails
// with "another postmaster still running" and returns the port that
// postmaster is listening on. Returns ok=false when the error isn't the
// already-running signal or the pid file is unreadable.
func detectPostmasterPort(dataDir string, startErr error) (uint32, bool) {
	if !strings.Contains(startErr.Error(), "Is another postmaster") {
		return 0, false
	}
	pidPath := filepath.Join(dataDir, "data", "postmaster.pid")
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, false
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) <= posmasterLinePort {
		return 0, false
	}
	p, err := strconv.ParseUint(strings.TrimSpace(lines[posmasterLinePort]), 10, 32)
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
