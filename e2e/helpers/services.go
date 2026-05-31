package helpers

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/flanksource/commons-db/db"
)

// ServiceManager starts the native services the e2e suite depends on.
//
// Only Postgres is started for real (via db.StartEmbedded). The other
// services (Redis, OpenSearch, Loki, LocalStack) are not yet implemented;
// the current e2e specs only assert on their connection-URL strings and on
// locally-generated fixture data, so no live instance is required. Their
// URL/port accessors remain so those specs keep compiling.
type ServiceManager struct {
	postgresDSN  string
	postgresPort int
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

func (sm *ServiceManager) startPostgres() error {
	dsn, stop, err := db.StartEmbedded(db.EmbeddedConfig{
		DataDir:  sm.tmpDir,
		Database: "test",
		Port:     0,
	})
	if err != nil {
		return err
	}

	port, err := portFromDSN(dsn)
	if err != nil {
		_ = stop()
		return err
	}

	sm.postgresDSN = dsn
	sm.postgresPort = port
	sm.postgresStop = stop
	return nil
}

func (sm *ServiceManager) isPostgresHealthy() bool {
	return sm.postgresPort != 0 && isPortHealthy(sm.postgresPort)
}

func isPortHealthy(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 1*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

// portFromDSN extracts the TCP port from a postgres:// DSN.
func portFromDSN(dsn string) (int, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return 0, fmt.Errorf("parse dsn: %w", err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		return 0, fmt.Errorf("dsn port %q: %w", u.Port(), err)
	}
	return p, nil
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
