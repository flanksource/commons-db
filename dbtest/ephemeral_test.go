package dbtest

import (
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gstruct"
)

var _ = Describe("ephemeral database configuration", func() {
	const (
		testHome   = "/home/tester"
		testUnique = "7_a1b2c3d4"
		testName   = "Migration Runner"
	)
	created := time.Date(2026, time.July, 27, 10, 30, 0, 0, time.UTC)

	It("places the PostgreSQL data directory below the requested config root", func() {
		root := embeddedRoot(testHome)
		Expect(root).To(Equal(filepath.Join(testHome, ".config", "commons-db")))
		Expect(filepath.Join(root, "data")).To(Equal(
			filepath.Join(testHome, ".config", "commons-db", "data"),
		))
		Expect(embeddedPort).To(Equal(7432))
	})

	It("creates parseable pool and checked-out database names", func() {
		poolName := managedDatabaseName(poolDatabasePrefix, created, testUnique, "")
		testDatabaseName := managedDatabaseName(testDatabasePrefix, created, testUnique, testName)

		Expect(poolName).To(HavePrefix(poolDatabasePrefix))
		Expect(testDatabaseName).To(HavePrefix(testDatabasePrefix))
		Expect(testDatabaseName).To(ContainSubstring("migration_runner"))
		Expect(len(poolName)).To(BeNumerically("<=", maxIdentifier))
		Expect(len(testDatabaseName)).To(BeNumerically("<=", maxIdentifier))

		poolCreated, err := managedDatabaseCreated(poolName)
		Expect(err).NotTo(HaveOccurred())
		Expect(poolCreated).To(Equal(created))

		testCreated, err := managedDatabaseCreated(testDatabaseName)
		Expect(err).NotTo(HaveOccurred())
		Expect(testCreated).To(Equal(created))
	})

	It("preserves the unique suffix when a descriptive name must be truncated", func() {
		name := managedDatabaseName(
			testDatabasePrefix,
			created,
			testUnique,
			strings.Repeat("long descriptive name ", 10),
		)

		Expect(len(name)).To(BeNumerically("<=", maxIdentifier))
		Expect(name).To(ContainSubstring(testUnique))
	})

	DescribeTable("rejects names outside the managed namespace",
		func(name string) {
			_, err := managedDatabaseCreated(name)
			Expect(err).To(MatchError(ContainSubstring("managed database name")))
		},
		Entry("unmanaged database", "application"),
		Entry("missing timestamp", poolDatabasePrefix+"not-a-time_"+testUnique),
	)

	It("marks only databases older than the 24-hour retention as stale", func() {
		name := managedDatabaseName(testDatabasePrefix, created, testUnique, testName)

		stale, err := managedDatabaseStale(name, created.Add(staleDatabaseAge), staleDatabaseAge)
		Expect(err).NotTo(HaveOccurred())
		Expect(stale).To(BeFalse())

		stale, err = managedDatabaseStale(
			name,
			created.Add(staleDatabaseAge+time.Second),
			staleDatabaseAge,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(stale).To(BeTrue())
	})

	It("uses batches of exactly five databases", func() {
		Expect(poolBatchSize).To(Equal(5))
	})

	It("accepts lowercase managed prefixes with separators", func() {
		pool, err := newEphemeralPool(ephemeralPoolConfig{
			AdminURL:   "postgres://postgres:postgres@localhost:7432/postgres?sslmode=disable",
			PoolPrefix: poolDatabasePrefix,
			TestPrefix: testDatabasePrefix,
			BatchSize:  poolBatchSize,
			MaxAge:     staleDatabaseAge,
			Now:        func() time.Time { return created },
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(pool.config).To(gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{
			"AdminURL":   Equal("postgres://postgres:postgres@localhost:7432/postgres?sslmode=disable"),
			"PoolPrefix": Equal(poolDatabasePrefix),
			"TestPrefix": Equal(testDatabasePrefix),
			"BatchSize":  Equal(poolBatchSize),
			"MaxAge":     Equal(staleDatabaseAge),
			"Now":        Not(BeNil()),
		}))
	})

	It("rejects overlapping managed database prefixes", func() {
		_, err := newEphemeralPool(ephemeralPoolConfig{
			AdminURL:   "postgres://postgres:postgres@localhost:7432/postgres?sslmode=disable",
			PoolPrefix: "commons_db_",
			TestPrefix: testDatabasePrefix,
			BatchSize:  poolBatchSize,
			MaxAge:     staleDatabaseAge,
			Now:        func() time.Time { return created },
		})

		Expect(err).To(MatchError(ContainSubstring("must not overlap")))
	})
})
