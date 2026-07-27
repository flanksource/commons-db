package dbtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	commonsdb "github.com/flanksource/commons-db/db"
)

const (
	embeddedPort       = 7432
	poolBatchSize      = 5
	staleDatabaseAge   = 24 * time.Hour
	poolDatabasePrefix = "commons_db_pool_"
	testDatabasePrefix = "commons_db_test_"
)

var managedPrefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*_$`)

type ephemeralPoolConfig struct {
	AdminURL   string
	PoolPrefix string
	TestPrefix string
	BatchSize  int
	MaxAge     time.Duration
	Now        func() time.Time
}

type ephemeralPool struct {
	config ephemeralPoolConfig
}

func embeddedRoot(home string) string {
	return filepath.Join(home, ".config", "commons-db")
}

func managedDatabaseName(prefix string, created time.Time, unique, name string) string {
	base := fmt.Sprintf("%s%d_%s", prefix, created.Unix(), unique)
	if len(base) > maxIdentifier {
		panic(fmt.Sprintf("dbtest: managed database identity exceeds %d characters: %q", maxIdentifier, base))
	}
	if name == "" {
		return base
	}
	// The identity is what keeps concurrent runs apart, so when it fills the
	// identifier there is no room left for a human-readable description.
	room := maxIdentifier - len(base) - 1
	if room <= 0 {
		return base
	}
	description := sanitize(name)
	if len(description) > room {
		description = description[:room]
	}
	return base + "_" + description
}

func managedDatabaseCreated(name string) (time.Time, error) {
	return managedDatabaseCreatedWithPrefixes(name, poolDatabasePrefix, testDatabasePrefix)
}

func managedDatabaseCreatedWithPrefixes(name, poolPrefix, testPrefix string) (time.Time, error) {
	var remainder string
	switch {
	case strings.HasPrefix(name, poolPrefix):
		remainder = strings.TrimPrefix(name, poolPrefix)
	case strings.HasPrefix(name, testPrefix):
		remainder = strings.TrimPrefix(name, testPrefix)
	default:
		return time.Time{}, fmt.Errorf("managed database name %q has an unknown prefix", name)
	}

	timestamp, suffix, ok := strings.Cut(remainder, "_")
	if !ok || suffix == "" {
		return time.Time{}, fmt.Errorf("managed database name %q is missing its identity", name)
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}, fmt.Errorf("managed database name %q has an invalid timestamp", name)
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func managedDatabaseStale(name string, now time.Time, maxAge time.Duration) (bool, error) {
	if maxAge <= 0 {
		return false, errors.New("managed database retention must be positive")
	}
	created, err := managedDatabaseCreated(name)
	if err != nil {
		return false, err
	}
	if created.After(now) {
		return false, fmt.Errorf("managed database name %q has a future timestamp", name)
	}
	return now.Sub(created) > maxAge, nil
}

func newEphemeralPool(config ephemeralPoolConfig) (*ephemeralPool, error) {
	if config.AdminURL == "" {
		return nil, errors.New("ephemeral pool admin URL is required")
	}
	if err := validateManagedPrefix(config.PoolPrefix, "pool"); err != nil {
		return nil, err
	}
	if err := validateManagedPrefix(config.TestPrefix, "test"); err != nil {
		return nil, err
	}
	if strings.HasPrefix(config.PoolPrefix, config.TestPrefix) ||
		strings.HasPrefix(config.TestPrefix, config.PoolPrefix) {
		return nil, errors.New("ephemeral pool and test database prefixes must not overlap")
	}
	if config.BatchSize <= 0 {
		return nil, errors.New("ephemeral pool batch size must be positive")
	}
	if config.MaxAge <= 0 {
		return nil, errors.New("ephemeral pool retention must be positive")
	}
	if config.Now == nil {
		return nil, errors.New("ephemeral pool clock is required")
	}
	return &ephemeralPool{config: config}, nil
}

func validateManagedPrefix(prefix, kind string) error {
	if prefix == "" {
		return fmt.Errorf("ephemeral %s database prefix is required", kind)
	}
	if !managedPrefixPattern.MatchString(prefix) {
		return fmt.Errorf("ephemeral %s database prefix %q must contain lowercase alphanumerics and end in underscore", kind, prefix)
	}
	if len(prefix) > maxIdentifier-24 {
		return fmt.Errorf("ephemeral %s database prefix %q leaves no room for an identity", kind, prefix)
	}
	return nil
}

func acquirePooledScratch(
	ctx context.Context,
	adminURL, name, unique string,
	now time.Time,
) (string, func() error, error) {
	pool, err := newEphemeralPool(ephemeralPoolConfig{
		AdminURL:   adminURL,
		PoolPrefix: poolDatabasePrefix,
		TestPrefix: testDatabasePrefix,
		BatchSize:  poolBatchSize,
		MaxAge:     staleDatabaseAge,
		Now:        func() time.Time { return now },
	})
	if err != nil {
		return "", nil, err
	}
	return pool.acquire(ctx, name, unique)
}

func startSharedEmbedded(dataDir string) (string, error) {
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory for embedded PostgreSQL: %w", err)
		}
		dataDir = embeddedRoot(home)
	}
	dsn, _, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:     dataDir,
		Database:    "postgres",
		Port:        embeddedPort,
		KeepRunning: true,
		FastTesting: true,
	})
	if err != nil {
		return "", fmt.Errorf("start shared embedded PostgreSQL: %w", err)
	}
	return dsn, nil
}
