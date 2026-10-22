package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sync/errgroup"
)

func TestEphemeralDatabases(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Ephemeral Database Suite")
}

const releaseHelperAdminURLEnv = "DBTEST_RELEASE_HELPER_ADMIN_URL"

// TestReleaseInBackgroundHelper is the child process for the "completes a
// background drop after the releasing process exits" spec: it leases a
// database, prints its name, releases it in the background and exits at once.
func TestReleaseInBackgroundHelper(t *testing.T) {
	adminURL := os.Getenv(releaseHelperAdminURLEnv)
	if adminURL == "" {
		return
	}
	dsn, lease, err := createLeasedDatabase(context.Background(), scratchRequest{
		adminURL: adminURL, name: "exit release", unique: uniqueSuffix(), now: time.Now(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Println(strings.TrimPrefix(parsed.Path, "/"))
	lease.releaseInBackground()
	os.Exit(0)
}

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

	It("creates the first lease directly and refills the pool to its batch size in the background", func(ctx SpecContext) {
		namespace := fmt.Sprintf("cdb_%x_", time.Now().UnixNano())
		poolPrefix := namespace + "pool_"
		testPrefix := namespace + "test_"
		pool, err := newEphemeralPool(ephemeralPoolConfig{
			AdminURL:   adminURL,
			PoolPrefix: poolPrefix,
			TestPrefix: testPrefix,
			BatchSize:  minPoolBatchSize,
			MaxAge:     staleDatabaseAge,
			Now:        time.Now,
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(dropPoolDatabases, pool)

		leases := make([]*databaseLease, 0, minPoolBatchSize+1)
		databases := make([]string, 0, minPoolBatchSize+1)
		acquire := func(i int) {
			dsn, lease, err := pool.acquire(ctx, "Migration Runner", fmt.Sprintf("lease_%d", i))
			Expect(err).NotTo(HaveOccurred())
			leases = append(leases, lease)
			databases = append(databases, databaseFromDSN(dsn))
		}

		acquire(0)
		Expect(databases[0]).To(HavePrefix(testPrefix))
		Eventually(func() int { return databaseCount(ctx, adminURL, poolPrefix) }).
			WithTimeout(connectTimeout).Should(Equal(minPoolBatchSize))
		waitForRefill(pool)

		for i := 1; i <= minPoolBatchSize; i++ {
			acquire(i)
		}
		Expect(uniqueStrings(databases)).To(HaveLen(minPoolBatchSize + 1))
		Eventually(func() int { return databaseCount(ctx, adminURL, poolPrefix) }).
			WithTimeout(connectTimeout).Should(Equal(minPoolBatchSize))

		for _, lease := range leases {
			Expect(lease.release()).To(Succeed())
		}
		Expect(databaseCount(ctx, adminURL, testPrefix)).To(BeZero())
	})

	It("hands concurrent callers on an empty pool distinct databases within connectTimeout, never creating one under the pool lock", func(ctx SpecContext) {
		namespace := fmt.Sprintf("cdb_%x_", time.Now().UnixNano())
		poolPrefix := namespace + "pool_"
		testPrefix := namespace + "test_"
		pool, err := newEphemeralPool(ephemeralPoolConfig{
			AdminURL:   adminURL,
			PoolPrefix: poolPrefix,
			TestPrefix: testPrefix,
			BatchSize:  minPoolBatchSize,
			MaxAge:     staleDatabaseAge,
			Now:        time.Now,
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(dropPoolDatabases, pool)

		expectLockVisibleToProbe(ctx, adminURL, pool.globalLock())
		probeContext, stopProbe := context.WithCancel(ctx)
		probe := make(chan createProbe, 1)
		go func() { probe <- probeCreatesUnderLock(probeContext, adminURL, pool.globalLock(), namespace) }()

		type checkout struct {
			dsn   string
			lease *databaseLease
		}
		checkouts := make([]checkout, minPoolBatchSize*2)
		acquireContext, cancel := context.WithTimeout(ctx, connectTimeout)
		defer cancel()
		group, groupContext := errgroup.WithContext(acquireContext)
		for i := range checkouts {
			group.Go(func() error {
				dsn, lease, err := pool.acquire(groupContext, "Concurrent Runner", fmt.Sprintf("lease_%d", i))
				checkouts[i] = checkout{dsn: dsn, lease: lease}
				return err
			})
		}
		Expect(group.Wait()).To(Succeed())
		waitForRefill(pool)
		stopProbe()
		observed := <-probe

		Expect(observed.err).NotTo(HaveOccurred())
		Expect(observed.creates).To(BeNumerically(">", 0), "the probe must see creates to prove anything")
		Expect(observed.createsUnderLock).To(BeZero())
		databases := make([]string, 0, len(checkouts))
		for _, checkout := range checkouts {
			databases = append(databases, databaseFromDSN(checkout.dsn))
			Expect(checkout.lease.release()).To(Succeed())
		}
		Expect(uniqueStrings(databases)).To(HaveLen(len(checkouts)))
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
			BatchSize:  minPoolBatchSize,
			MaxAge:     staleDatabaseAge,
			Now:        func() time.Time { return clock },
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(dropPoolDatabases, pool)

		activeDSN, activeLease, err := pool.acquire(ctx, "active", "lease_active")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(activeLease.release()).To(Succeed()) })
		activeDatabase := databaseFromDSN(activeDSN)

		abandonedDatabase := managedDatabaseName(testPrefix, old, "lease_abandoned", "abandoned")
		Expect(createDatabase(ctx, adminURL, abandonedDatabase)).To(Succeed())

		waitForRefill(pool)
		clock = now
		Expect(pool.sweep(ctx)).To(Succeed())

		Expect(databaseExists(ctx, adminURL, activeDatabase)).To(BeTrue())
		Expect(databaseExists(ctx, adminURL, abandonedDatabase)).To(BeFalse())
	})

	It("sweeps an unlocked test database once the grace period passes, without waiting for retention", func(ctx SpecContext) {
		namespace := fmt.Sprintf("cdb_%x_", time.Now().UnixNano())
		poolPrefix := namespace + "pool_"
		testPrefix := namespace + "test_"
		now := time.Now().UTC().Truncate(time.Second)
		pool, err := newEphemeralPool(ephemeralPoolConfig{
			AdminURL:   adminURL,
			PoolPrefix: poolPrefix,
			TestPrefix: testPrefix,
			BatchSize:  minPoolBatchSize,
			MaxAge:     staleDatabaseAge,
			Now:        func() time.Time { return now },
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(dropManagedDatabases, adminURL, poolPrefix, testPrefix)

		abandoned := managedDatabaseName(testPrefix, now.Add(-abandonedDatabaseGrace-time.Minute), "lease_abandoned", "abandoned")
		withinGrace := managedDatabaseName(testPrefix, now.Add(-time.Minute), "lease_recent", "recent")
		Expect(createDatabase(ctx, adminURL, abandoned)).To(Succeed())
		Expect(createDatabase(ctx, adminURL, withinGrace)).To(Succeed())

		Expect(pool.sweep(ctx)).To(Succeed())

		Expect(databaseExists(ctx, adminURL, abandoned)).To(BeFalse())
		Expect(databaseExists(ctx, adminURL, withinGrace)).To(BeTrue())
	})

	It("removes expired free pool databases only while no checkout holds the pool", func(ctx SpecContext) {
		namespace := fmt.Sprintf("cdb_%x_", time.Now().UnixNano())
		poolPrefix := namespace + "pool_"
		testPrefix := namespace + "test_"
		now := time.Now().UTC().Truncate(time.Second)
		pool, err := newEphemeralPool(ephemeralPoolConfig{
			AdminURL:   adminURL,
			PoolPrefix: poolPrefix,
			TestPrefix: testPrefix,
			BatchSize:  minPoolBatchSize,
			MaxAge:     staleDatabaseAge,
			Now:        func() time.Time { return now },
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(dropManagedDatabases, adminURL, poolPrefix, testPrefix)

		expired := managedDatabaseName(poolPrefix, now.Add(-staleDatabaseAge-time.Hour), "free_expired", "")
		fresh := managedDatabaseName(poolPrefix, now.Add(-time.Hour), "free_fresh", "")
		Expect(createDatabase(ctx, adminURL, expired)).To(Succeed())
		Expect(createDatabase(ctx, adminURL, fresh)).To(Succeed())

		checkout, err := openPoolSession(ctx, adminURL)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(checkout.close()).To(Succeed()) })
		Expect(checkout.lock(ctx, pool.globalLock())).To(Succeed())
		Expect(pool.sweep(ctx)).To(Succeed())
		Expect(databaseExists(ctx, adminURL, expired)).To(BeTrue())

		Expect(checkout.unlock(ctx, pool.globalLock())).To(Succeed())
		Expect(pool.sweep(ctx)).To(Succeed())
		Expect(databaseExists(ctx, adminURL, expired)).To(BeFalse())
		Expect(databaseExists(ctx, adminURL, fresh)).To(BeTrue())
	})

	It("completes a background drop after the releasing process exits", func(ctx SpecContext) {
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReleaseInBackgroundHelper$")
		command.Env = append(os.Environ(), releaseHelperAdminURLEnv+"="+adminURL)
		output, err := command.Output()
		Expect(err).NotTo(HaveOccurred())
		database := strings.TrimSpace(string(output))
		Expect(database).To(HavePrefix(testDatabasePrefix))
		DeferCleanup(dropManagedDatabases, adminURL, database, database)

		Eventually(func() bool { return databaseExists(ctx, adminURL, database) }).
			WithTimeout(10 * time.Second).
			Should(BeFalse())
	})

	It("leases a managed database on a configured server and drops it on cleanup", func(ctx SpecContext) {
		dsn, lease, err := createConfiguredScratch(ctx, adminURL, Options{Name: "Configured Runner"}, uniqueSuffix())
		Expect(err).NotTo(HaveOccurred())
		database := databaseFromDSN(dsn)
		DeferCleanup(dropManagedDatabases, adminURL, database, database)

		Expect(database).To(HavePrefix(testDatabasePrefix))
		Expect(database).To(ContainSubstring("configured_runner"))
		probe, err := openPoolSession(ctx, adminURL)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(probe.close()).To(Succeed()) })
		Expect(probe.tryLock(ctx, database)).To(BeFalse(), "a live lease must hold its lock")

		Expect(lease.release()).To(Succeed())
		Expect(databaseExists(ctx, adminURL, database)).To(BeFalse())
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

// waitForRefill blocks until no background refill of pool is running, so a
// test's databases are not recreated after it drops them.
func waitForRefill(pool *ephemeralPool) {
	Eventually(pool.refillState().running.Load).WithTimeout(sweepTimeout).Should(BeFalse())
}

func dropPoolDatabases(pool *ephemeralPool) {
	waitForRefill(pool)
	dropManagedDatabases(pool.config.AdminURL, pool.config.PoolPrefix, pool.config.TestPrefix)
}

// advisoryLockHeld is true when the backend whose pid fills %s holds the
// advisory lock named $1.
const advisoryLockHeld = `EXISTS (
	SELECT 1 FROM pg_locks l
	WHERE l.pid = %s AND l.locktype = 'advisory' AND l.granted AND l.objsubid = 1
	  AND l.classid::bigint = (hashtextextended($1, 0) >> 32) & 4294967295
	  AND l.objid::bigint = hashtextextended($1, 0) & 4294967295)`

// expectLockVisibleToProbe proves the probe's lock decoding by holding lock and
// finding it in pg_locks.
func expectLockVisibleToProbe(ctx context.Context, adminURL, lock string) {
	session, err := openPoolSession(ctx, adminURL)
	Expect(err).NotTo(HaveOccurred())
	defer session.close() //nolint:errcheck
	Expect(session.lock(ctx, lock)).To(Succeed())
	var held bool
	Expect(session.conn.QueryRowContext(ctx, "SELECT "+fmt.Sprintf(advisoryLockHeld, "pg_backend_pid()"), lock).Scan(&held)).To(Succeed())
	Expect(held).To(BeTrue())
	Expect(session.unlock(ctx, lock)).To(Succeed())
}

type createProbe struct {
	creates, createsUnderLock int
	err                       error
}

// probeCreatesUnderLock samples active CREATE DATABASE statements for names
// containing namespace until ctx ends, counting those whose backend holds the
// advisory lock named lock. pg_advisory_lock(bigint) stores the key's high and
// low 32 bits in classid and objid, with objsubid 1.
func probeCreatesUnderLock(ctx context.Context, adminURL, lock, namespace string) createProbe {
	admin, err := sql.Open("postgres", adminURL)
	if err != nil {
		return createProbe{err: err}
	}
	defer admin.Close() //nolint:errcheck
	var result createProbe
	for ctx.Err() == nil {
		var creates, underLock int
		err := admin.QueryRowContext(ctx, `
			SELECT count(*), count(*) FILTER (WHERE `+fmt.Sprintf(advisoryLockHeld, "a.pid")+`)
			FROM pg_stat_activity a
			WHERE a.state = 'active' AND a.query ILIKE 'CREATE DATABASE%' AND strpos(a.query, $2) > 0`,
			lock, namespace,
		).Scan(&creates, &underLock)
		if err != nil && ctx.Err() == nil {
			return createProbe{err: err}
		}
		result.creates += creates
		result.createsUnderLock += underLock
	}
	return result
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
