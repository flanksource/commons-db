package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sync/errgroup"
)

var _ = Describe("pooled ephemeral databases", Label("integration"), Ordered, func() {
	var adminURL string

	BeforeAll(func() {
		adminURL = os.Getenv(EnvURL)
		if adminURL != "" {
			return
		}
		var err error
		adminURL, err = startSharedEmbedded("")
		Expect(err).NotTo(HaveOccurred())
	})

	It("checks out distinct databases and replenishes only in batches of five", func(ctx SpecContext) {
		namespace := fmt.Sprintf("cdb_%x_", time.Now().UnixNano())
		poolPrefix := namespace + "pool_"
		testPrefix := namespace + "test_"
		pool, err := newEphemeralPool(ephemeralPoolConfig{
			AdminURL:   adminURL,
			PoolPrefix: poolPrefix,
			TestPrefix: testPrefix,
			BatchSize:  poolBatchSize,
			MaxAge:     staleDatabaseAge,
			Now:        time.Now,
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(dropManagedDatabases, adminURL, poolPrefix, testPrefix)

		leases := make([]func() error, 0, poolBatchSize+1)
		databases := make([]string, 0, poolBatchSize+1)
		for i := 0; i < poolBatchSize+1; i++ {
			dsn, cleanup, err := pool.acquire(ctx, "Migration Runner", fmt.Sprintf("lease_%d", i))
			Expect(err).NotTo(HaveOccurred())
			leases = append(leases, cleanup)
			databases = append(databases, databaseFromDSN(dsn))

			if i == 0 {
				Expect(databaseCount(ctx, adminURL, poolPrefix)).To(Equal(poolBatchSize - 1))
			}
			if i == poolBatchSize-1 {
				Expect(databaseCount(ctx, adminURL, poolPrefix)).To(BeZero())
			}
		}
		Expect(databaseCount(ctx, adminURL, poolPrefix)).To(Equal(poolBatchSize - 1))
		Expect(databases).To(HaveLen(poolBatchSize + 1))
		Expect(uniqueStrings(databases)).To(HaveLen(poolBatchSize + 1))

		for _, cleanup := range leases {
			Expect(cleanup()).To(Succeed())
		}
		Expect(databaseCount(ctx, adminURL, testPrefix)).To(BeZero())
	})

	It("hands out distinct databases to concurrent callers", func(ctx SpecContext) {
		namespace := fmt.Sprintf("cdb_%x_", time.Now().UnixNano())
		poolPrefix := namespace + "pool_"
		testPrefix := namespace + "test_"
		pool, err := newEphemeralPool(ephemeralPoolConfig{
			AdminURL:   adminURL,
			PoolPrefix: poolPrefix,
			TestPrefix: testPrefix,
			BatchSize:  poolBatchSize,
			MaxAge:     staleDatabaseAge,
			Now:        time.Now,
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(dropManagedDatabases, adminURL, poolPrefix, testPrefix)

		type lease struct {
			dsn     string
			cleanup func() error
		}
		leases := make([]lease, poolBatchSize*2)
		group, groupContext := errgroup.WithContext(ctx)
		for i := range leases {
			group.Go(func() error {
				dsn, cleanup, err := pool.acquire(
					groupContext,
					"Concurrent Runner",
					fmt.Sprintf("lease_%d", i),
				)
				leases[i] = lease{dsn: dsn, cleanup: cleanup}
				return err
			})
		}
		Expect(group.Wait()).To(Succeed())

		databases := make([]string, 0, len(leases))
		for _, lease := range leases {
			databases = append(databases, databaseFromDSN(lease.dsn))
			Expect(lease.cleanup()).To(Succeed())
		}
		Expect(uniqueStrings(databases)).To(HaveLen(len(leases)))
		Expect(databaseCount(ctx, adminURL, testPrefix)).To(BeZero())
	})

	It("keeps an active old lease and removes an abandoned old database", func(ctx SpecContext) {
		namespace := fmt.Sprintf("cdb_%x_", time.Now().UnixNano())
		poolPrefix := namespace + "pool_"
		testPrefix := namespace + "test_"
		now := time.Now().UTC().Truncate(time.Second)
		old := now.Add(-staleDatabaseAge - time.Hour)
		clock := old
		pool, err := newEphemeralPool(ephemeralPoolConfig{
			AdminURL:   adminURL,
			PoolPrefix: poolPrefix,
			TestPrefix: testPrefix,
			BatchSize:  poolBatchSize,
			MaxAge:     staleDatabaseAge,
			Now:        func() time.Time { return clock },
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(dropManagedDatabases, adminURL, poolPrefix, testPrefix)

		activeDSN, activeCleanup, err := pool.acquire(ctx, "active", "lease_active")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(activeCleanup()).To(Succeed()) })
		activeDatabase := databaseFromDSN(activeDSN)

		abandonedDatabase := managedDatabaseName(testPrefix, old, "lease_abandoned", "abandoned")
		Expect(createDatabase(ctx, adminURL, abandonedDatabase)).To(Succeed())

		clock = now
		_, triggerCleanup, err := pool.acquire(ctx, "trigger", "lease_trigger")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(triggerCleanup()).To(Succeed()) })

		Expect(databaseExists(ctx, adminURL, activeDatabase)).To(BeTrue())
		Expect(databaseExists(ctx, adminURL, abandonedDatabase)).To(BeFalse())
	})
})

func databaseFromDSN(dsn string) string {
	parsed, err := url.Parse(dsn)
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimPrefix(parsed.Path, "/")
}

func databaseCount(ctx context.Context, adminURL, prefix string) int {
	admin, err := sql.Open("postgres", adminURL)
	Expect(err).NotTo(HaveOccurred())
	defer admin.Close() //nolint:errcheck

	var count int
	Expect(admin.QueryRowContext(
		ctx,
		"SELECT count(*) FROM pg_database WHERE left(datname, length($1)) = $1",
		prefix,
	).Scan(&count)).To(Succeed())
	return count
}

func databaseExists(ctx context.Context, adminURL, name string) bool {
	admin, err := sql.Open("postgres", adminURL)
	Expect(err).NotTo(HaveOccurred())
	defer admin.Close() //nolint:errcheck

	var exists bool
	Expect(admin.QueryRowContext(
		ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)",
		name,
	).Scan(&exists)).To(Succeed())
	return exists
}

func createDatabase(ctx context.Context, adminURL, name string) error {
	admin, err := sql.Open("postgres", adminURL)
	if err != nil {
		return err
	}
	defer admin.Close() //nolint:errcheck
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+pq.QuoteIdentifier(name))
	return err
}

func dropManagedDatabases(adminURL string, prefixes ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := sql.Open("postgres", adminURL)
	Expect(err).NotTo(HaveOccurred())
	defer admin.Close() //nolint:errcheck

	rows, err := admin.QueryContext(
		ctx,
		"SELECT datname FROM pg_database WHERE left(datname, length($1)) = $1 OR left(datname, length($2)) = $2",
		prefixes[0],
		prefixes[1],
	)
	Expect(err).NotTo(HaveOccurred())

	var names []string
	for rows.Next() {
		var name string
		Expect(rows.Scan(&name)).To(Succeed())
		names = append(names, name)
	}
	Expect(rows.Close()).To(Succeed())
	Expect(rows.Err()).NotTo(HaveOccurred())
	sort.Strings(names)
	for _, name := range names {
		_, err := admin.ExecContext(ctx, "DROP DATABASE "+pq.QuoteIdentifier(name)+" WITH (FORCE)")
		Expect(err).NotTo(HaveOccurred())
	}
}

func uniqueStrings(values []string) []string {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		unique[value] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	return result
}
