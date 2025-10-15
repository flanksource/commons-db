package helpers

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"
)

type ServiceManager struct {
	postgresCmd   *exec.Cmd
	redisCmd      *exec.Cmd
	opensearchCmd *exec.Cmd
	lokiCmd       *exec.Cmd
	localstackCmd *exec.Cmd

	postgresPort   int
	redisPort      int
	opensearchPort int
	lokiPort       int
	localstackPort int

	tmpDir string
}

func NewServiceManager() *ServiceManager {
	return &ServiceManager{
		postgresPort:   5432,
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

	// Start services in order
	if err := sm.startPostgres(ctx); err != nil {
		return fmt.Errorf("failed to start postgres: %w", err)
	}

	if err := sm.startRedis(ctx); err != nil {
		return fmt.Errorf("failed to start redis: %w", err)
	}

	if err := sm.startOpenSearch(ctx); err != nil {
		return fmt.Errorf("failed to start opensearch: %w", err)
	}

	if err := sm.startLoki(ctx); err != nil {
		return fmt.Errorf("failed to start loki: %w", err)
	}

	if err := sm.startLocalStack(ctx); err != nil {
		return fmt.Errorf("failed to start localstack: %w", err)
	}

	return nil
}

func (sm *ServiceManager) StopAll(ctx context.Context) error {
	var errs []error

	if sm.postgresCmd != nil && sm.postgresCmd.Process != nil {
		_ = sm.postgresCmd.Process.Kill()
	}

	if sm.redisCmd != nil && sm.redisCmd.Process != nil {
		_ = sm.redisCmd.Process.Kill()
	}

	if sm.opensearchCmd != nil && sm.opensearchCmd.Process != nil {
		_ = sm.opensearchCmd.Process.Kill()
	}

	if sm.lokiCmd != nil && sm.lokiCmd.Process != nil {
		_ = sm.lokiCmd.Process.Kill()
	}

	if sm.localstackCmd != nil && sm.localstackCmd.Process != nil {
		_ = sm.localstackCmd.Process.Kill()
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

func (sm *ServiceManager) AllHealthy() bool {
	return sm.isPostgresHealthy() && sm.isRedisHealthy() && sm.isOpenSearchHealthy() && sm.isLokiHealthy() && sm.isLocalStackHealthy()
}

func (sm *ServiceManager) startPostgres(ctx context.Context) error {
	if !sm.isPortAvailable(sm.postgresPort) {
		return fmt.Errorf("postgres port %d not available", sm.postgresPort)
	}

	// TODO: Implement postgres startup using embedded binaries from deps
	// This is a placeholder that would use zonky postgres binaries
	return nil
}

func (sm *ServiceManager) startRedis(ctx context.Context) error {
	if !sm.isPortAvailable(sm.redisPort) {
		return fmt.Errorf("redis port %d not available", sm.redisPort)
	}

	// TODO: Implement redis startup
	return nil
}

func (sm *ServiceManager) startOpenSearch(ctx context.Context) error {
	if !sm.isPortAvailable(sm.opensearchPort) {
		return fmt.Errorf("opensearch port %d not available", sm.opensearchPort)
	}

	// TODO: Implement opensearch startup
	return nil
}

func (sm *ServiceManager) startLoki(ctx context.Context) error {
	if !sm.isPortAvailable(sm.lokiPort) {
		return fmt.Errorf("loki port %d not available", sm.lokiPort)
	}

	// TODO: Implement loki startup
	return nil
}

func (sm *ServiceManager) startLocalStack(ctx context.Context) error {
	if !sm.isPortAvailable(sm.localstackPort) {
		return fmt.Errorf("localstack port %d not available", sm.localstackPort)
	}

	// TODO: Implement localstack startup
	return nil
}

func (sm *ServiceManager) isPostgresHealthy() bool {
	return sm.isPortHealthy(sm.postgresPort)
}

func (sm *ServiceManager) isRedisHealthy() bool {
	return sm.isPortHealthy(sm.redisPort)
}

func (sm *ServiceManager) isOpenSearchHealthy() bool {
	return sm.isPortHealthy(sm.opensearchPort)
}

func (sm *ServiceManager) isLokiHealthy() bool {
	return sm.isPortHealthy(sm.lokiPort)
}

func (sm *ServiceManager) isLocalStackHealthy() bool {
	return sm.isPortHealthy(sm.localstackPort)
}

func (sm *ServiceManager) isPortHealthy(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 1*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

func (sm *ServiceManager) isPortAvailable(port int) bool {
	conn, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

func (sm *ServiceManager) PostgresURL() string {
	return fmt.Sprintf("postgres://localhost:%d/test", sm.postgresPort)
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
