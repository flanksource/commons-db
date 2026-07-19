package helpers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/flanksource/commons-db/dbtest"
	_ "github.com/lib/pq"
)

// ServiceManager starts the native services the e2e suite depends on.
//
// Only Postgres is started for real (via dbtest, so COMMONS_DB_URL points the
// suite at an external server). The other services (Redis, OpenSearch, Loki,
// LocalStack) are not yet implemented; the current e2e specs only assert on
// their connection-URL strings and on locally-generated fixture data, so no
// live instance is required. Their URL/port accessors remain so those specs
// keep compiling.
type ServiceManager struct {
	postgresDSN  string
	postgresStop func() error

	redisPort      int
	opensearchPort int
	lokiPort       int
	localstackPort int

	tmpDir string
}

func NewServiceManager() *ServiceManager {
	return &ServiceManager{
		redisPort:      6379,
		opensearchPort: 9200,
		lokiPort:       3100,
		localstackPort: 4566,
	}
}

func (sm *ServiceManager) StartAll(ctx context.Context) error {
	tmpDir, err := os.MkdirTemp("", "e2e-services-")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	sm.tmpDir = tmpDir

	if err := sm.startPostgres(); err != nil {
		return fmt.Errorf("failed to start postgres: %w", err)
	}

	return nil
}

func (sm *ServiceManager) StopAll(ctx context.Context) error {
	var errs []error

	if sm.postgresStop != nil {
		if err := sm.postgresStop(); err != nil {
			errs = append(errs, fmt.Errorf("stop postgres: %w", err))
		}
	}

	if sm.tmpDir != "" {
		if err := os.RemoveAll(sm.tmpDir); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during cleanup: %v", errs)
	}

	return nil
}

// AllHealthy reports whether every service StartAll actually started is
// reachable. Only Postgres is started today, so only Postgres is checked.
func (sm *ServiceManager) AllHealthy() bool {
	return sm.isPostgresHealthy()
}

// startPostgres resolves the suite's database from the environment. When
// neither COMMONS_DB_URL nor COMMONS_DB_EMBEDDED_TEST is set there is no
// database to start; that is not an error, but PostgresURL then reports the
// empty string and PostgresEnabled reports false, so a spec that needs one
// must skip rather than silently connect to nothing.
func (sm *ServiceManager) startPostgres() error {
	handle, stop, err := dbtest.Open(dbtest.Options{Name: "e2e", DataDir: sm.tmpDir})
	if errors.Is(err, dbtest.ErrSkip) {
		return nil
	}
	if err != nil {
		return err
	}

	sm.postgresDSN = handle.DSN()
	sm.postgresStop = stop
	return nil
}

// PostgresEnabled reports whether a database was resolved for this suite.
func (sm *ServiceManager) PostgresEnabled() bool { return sm.postgresDSN != "" }

// isPostgresHealthy connects rather than dialling a port: COMMONS_DB_URL may
// point at any host, and a DSN that omits the port still resolves to 5432.
func (sm *ServiceManager) isPostgresHealthy() bool {
	if !sm.PostgresEnabled() {
		return true // nothing was started, so nothing can be unhealthy
	}
	conn, err := sql.Open("postgres", sm.postgresDSN)
	if err != nil {
		return false
	}
	defer conn.Close() //nolint:errcheck
	return conn.Ping() == nil
}

func (sm *ServiceManager) PostgresURL() string {
	return sm.postgresDSN
}

func (sm *ServiceManager) RedisURL() string {
	return fmt.Sprintf("redis://localhost:%d", sm.redisPort)
}

func (sm *ServiceManager) OpenSearchURL() string {
	return fmt.Sprintf("http://localhost:%d", sm.opensearchPort)
}

func (sm *ServiceManager) LokiURL() string {
	return fmt.Sprintf("http://localhost:%d", sm.lokiPort)
}

func (sm *ServiceManager) LocalStackURL() string {
	return fmt.Sprintf("http://localhost:%d", sm.localstackPort)
}

func (sm *ServiceManager) TmpDir() string {
	return sm.tmpDir
}
