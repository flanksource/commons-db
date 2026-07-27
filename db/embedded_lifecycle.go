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
	"sync"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/flanksource/commons/logger"
	"github.com/gofrs/flock"
	"github.com/jackc/pgx/v5"
)

const (
	embeddedStartupLockTimeout = 5 * time.Minute
	embeddedStartupRetryDelay  = 100 * time.Millisecond
	embeddedProbeTimeout       = 200 * time.Millisecond
)

var embeddedStartupMu sync.Mutex

type embeddedRuntime struct {
	config   EmbeddedConfig
	port     uint32
	dataPath string
	version  embeddedpostgres.PostgresVersion
	logger   io.Writer
}

// StartEmbedded launches PostgreSQL under cfg.DataDir. Reused servers and
// servers configured with KeepRunning are never stopped by the returned closer.
func StartEmbedded(cfg EmbeddedConfig) (string, func() error, error) {
	runtime, err := newEmbeddedRuntime(cfg)
	if err != nil {
		return "", nil, err
	}
	return runtime.withStartupLock(runtime.startOrReuse)
}

func newEmbeddedRuntime(cfg EmbeddedConfig) (*embeddedRuntime, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("EmbeddedConfig.DataDir is required")
	}
	applyEmbeddedDefaults(&cfg)
	if err := createEmbeddedDirectories(cfg.DataDir); err != nil {
		return nil, err
	}

	dataPath := filepath.Join(cfg.DataDir, "data")
	pgLog := cfg.Logger
	if pgLog == nil {
		pgLog = os.Stderr
	}
	return &embeddedRuntime{
		config:   cfg,
		port:     cfg.Port,
		dataPath: dataPath,
		logger:   pgLog,
	}, nil
}

func applyEmbeddedDefaults(cfg *EmbeddedConfig) {
	if cfg.Database == "" {
		cfg.Database = "postgres"
	}
	if cfg.Username == "" {
		cfg.Username = "postgres"
	}
	if cfg.Password == "" {
		cfg.Password = "postgres"
	}
}

func createEmbeddedDirectories(root string) error {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return fmt.Errorf("create embedded PostgreSQL root %s: %w", root, err)
	}
	if err := os.Chmod(root, 0o750); err != nil {
		return fmt.Errorf("set embedded PostgreSQL root permissions on %s: %w", root, err)
	}
	dataPath := filepath.Join(root, "data")
	if err := os.MkdirAll(dataPath, 0o750); err != nil {
		return fmt.Errorf("create embedded PostgreSQL data directory %s: %w", dataPath, err)
	}
	return nil
}

func resolveEmbeddedPort(cfg EmbeddedConfig, dataPath string) (uint32, error) {
	if cfg.Port != 0 {
		return cfg.Port, nil
	}
	if port, ok := readPostmasterPort(dataPath); ok {
		return port, nil
	}
	port, err := FreePort()
	if err != nil {
		return 0, fmt.Errorf("pick free port: %w", err)
	}
	return uint32(port), nil //nolint:gosec // FreePort always returns a valid TCP port
}

func (r *embeddedRuntime) withStartupLock(
	fn func() (string, func() error, error),
) (string, func() error, error) {
	embeddedStartupMu.Lock()
	defer embeddedStartupMu.Unlock()

	lock := flock.New(filepath.Join(r.config.DataDir, "embedded-postgres.lock"))
	ctx, cancel := context.WithTimeout(context.Background(), embeddedStartupLockTimeout)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, embeddedStartupRetryDelay)
	if err != nil {
		return "", nil, fmt.Errorf("lock embedded PostgreSQL startup: %w", err)
	}
	if !locked {
		return "", nil, fmt.Errorf("lock embedded PostgreSQL startup: %w", ctx.Err())
	}

	dsn, stop, startErr := fn()
	unlockErr := lock.Unlock()
	if startErr != nil {
		return "", nil, errors.Join(startErr, unlockErr)
	}
	if unlockErr != nil {
		// The server did start, so its stop closer must not be dropped on the
		// floor: shut it back down rather than leaking an unmanaged cluster.
		return "", nil, errors.Join(unlockErr, stop())
	}
	return dsn, stop, nil
}

func (r *embeddedRuntime) startOrReuse() (string, func() error, error) {
	if err := r.resolveLockedConfig(); err != nil {
		return "", nil, err
	}
	dsn := r.dsn()
	reused, err := r.reuseRunningServer(dsn)
	if err != nil {
		return "", nil, err
	}
	if reused {
		return dsn, noOpEmbeddedStop, nil
	}

	logger.Infof(
		"Starting embedded postgres at %s (version %s, port %d)",
		r.config.DataDir,
		r.version,
		r.port,
	)
	server := embeddedpostgres.NewDatabase(r.serverConfig())
	if err := server.Start(); err != nil {
		return "", nil, fmt.Errorf("start embedded postgres: %w", err)
	}
	if err := r.validateStartedServer(dsn); err != nil {
		return "", nil, errors.Join(err, server.Stop())
	}
	if r.config.KeepRunning {
		return dsn, noOpEmbeddedStop, nil
	}
	return dsn, server.Stop, nil
}

func (r *embeddedRuntime) resolveLockedConfig() error {
	if r.port == 0 {
		port, err := resolveEmbeddedPort(r.config, r.dataPath)
		if err != nil {
			return err
		}
		r.port = port
	}
	version, err := detectPGVersion(r.dataPath)
	if err != nil {
		return err
	}
	r.version = version
	return nil
}

func (r *embeddedRuntime) reuseRunningServer(dsn string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), embeddedProbeTimeout)
	defer cancel()
	if !portIsListening(ctx, r.port) {
		return false, nil
	}
	if err := r.validateServer(dsn); err != nil {
		return false, fmt.Errorf(
			"port %d is occupied by an incompatible PostgreSQL server: %w",
			r.port,
			err,
		)
	}
	logger.Infof("reusing existing embedded postgres on port %d", r.port)
	return true, nil
}

func (r *embeddedRuntime) validateStartedServer(dsn string) error {
	if err := waitReady(dsn, 10*time.Second); err != nil {
		return fmt.Errorf("embedded postgres never became ready: %w", err)
	}
	return r.validateServer(dsn)
}

func (r *embeddedRuntime) validateServer(dsn string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to embedded PostgreSQL: %w", err)
	}
	defer conn.Close(context.Background()) //nolint:errcheck

	if err := validateEmbeddedDataDirectory(ctx, conn, r.dataPath); err != nil {
		return err
	}
	if r.config.FastTesting {
		if err := validateFastTestingServer(ctx, conn); err != nil {
			return err
		}
	}
	if r.config.PerformanceDiagnostics {
		if err := EnsurePerformanceDiagnostics(ctx, dsn); err != nil {
			return err
		}
	}
	return nil
}

func validateEmbeddedDataDirectory(ctx context.Context, conn *pgx.Conn, expected string) error {
	var actual string
	if err := conn.QueryRow(ctx, "SHOW data_directory").Scan(&actual); err != nil {
		return fmt.Errorf("read embedded PostgreSQL data_directory: %w", err)
	}
	expectedPath, err := filepath.EvalSymlinks(expected)
	if err != nil {
		return fmt.Errorf("resolve expected PostgreSQL data directory %s: %w", expected, err)
	}
	actualPath, err := filepath.EvalSymlinks(actual)
	if err != nil {
		return fmt.Errorf("resolve active PostgreSQL data directory %s: %w", actual, err)
	}
	if actualPath != expectedPath {
		return fmt.Errorf("expected PostgreSQL data_directory %s, got %s", expectedPath, actualPath)
	}
	return nil
}

func validateFastTestingServer(ctx context.Context, conn *pgx.Conn) error {
	var fsync, synchronousCommit, fullPageWrites string
	if err := conn.QueryRow(ctx, "SHOW fsync").Scan(&fsync); err != nil {
		return fmt.Errorf("read fsync: %w", err)
	}
	if err := conn.QueryRow(ctx, "SHOW synchronous_commit").Scan(&synchronousCommit); err != nil {
		return fmt.Errorf("read synchronous_commit: %w", err)
	}
	if err := conn.QueryRow(ctx, "SHOW full_page_writes").Scan(&fullPageWrites); err != nil {
		return fmt.Errorf("read full_page_writes: %w", err)
	}
	maxConnections, err := showInt(ctx, conn, "max_connections")
	if err != nil {
		return err
	}
	return validateFastTestingSettings(fsync, synchronousCommit, fullPageWrites, maxConnections)
}

// showInt reads a numeric server setting. SHOW always reports text (OID 25)
// regardless of the underlying GUC type, so the value has to be parsed rather
// than scanned straight into an int. setting is always a package-level literal;
// SHOW takes no bind parameters.
func showInt(ctx context.Context, conn *pgx.Conn, setting string) (int, error) {
	var raw string
	if err := conn.QueryRow(ctx, "SHOW "+setting).Scan(&raw); err != nil {
		return 0, fmt.Errorf("read %s: %w", setting, err)
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s %q: %w", setting, raw, err)
	}
	return value, nil
}

func (r *embeddedRuntime) serverConfig() embeddedpostgres.Config {
	config := embeddedpostgres.DefaultConfig().
		Port(r.port).
		DataPath(r.dataPath).
		RuntimePath(filepath.Join(r.config.DataDir, "runtime")).
		BinariesPath(filepath.Join(r.config.DataDir, "bin", string(r.version))).
		Version(r.version).
		Username(r.config.Username).
		Password(r.config.Password).
		Database(r.config.Database).
		Logger(r.logger)
	if parameters := embeddedStartParameters(r.config); len(parameters) > 0 {
		config = config.StartParameters(parameters)
	}
	return config
}

func embeddedStartParameters(cfg EmbeddedConfig) map[string]string {
	parameters := fastTestingStartParameters(cfg.FastTesting)
	for key, value := range performanceDiagnosticStartParameters(cfg.PerformanceDiagnostics) {
		if parameters == nil {
			parameters = make(map[string]string)
		}
		parameters[key] = value
	}
	return parameters
}

func (r *embeddedRuntime) dsn() string {
	return fmt.Sprintf(
		"postgres://%s:%s@localhost:%d/%s?sslmode=disable",
		r.config.Username,
		r.config.Password,
		r.port,
		r.config.Database,
	)
}

func portIsListening(ctx context.Context, port uint32) bool {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func noOpEmbeddedStop() error {
	return nil
}
