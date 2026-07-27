package db

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("embedded PostgreSQL lifecycle", Label("integration"), func() {
	It("reuses a fixed-port fast server from the same data directory", func() {
		if os.Getenv("COMMONS_DB_EMBEDDED_TEST") == "" {
			Skip("set COMMONS_DB_EMBEDDED_TEST=1 to run embedded PostgreSQL lifecycle tests")
		}

		port, err := FreePort()
		Expect(err).NotTo(HaveOccurred())

		config := EmbeddedConfig{
			DataDir:     GinkgoT().TempDir(),
			Database:    "postgres",
			Port:        uint32(port), //nolint:gosec // FreePort always returns a valid TCP port
			FastTesting: true,
		}
		dsn, stopOwner, err := StartEmbedded(config)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(stopOwner()).To(Succeed()) })
		postmasterBefore, err := os.ReadFile(filepath.Join(config.DataDir, "data", "postmaster.pid"))
		Expect(err).NotTo(HaveOccurred())

		reusedDSN, stopReuser, err := StartEmbedded(config)
		Expect(err).NotTo(HaveOccurred())
		Expect(reusedDSN).To(Equal(dsn))
		Expect(stopReuser()).To(Succeed())
		postmasterAfter, err := os.ReadFile(filepath.Join(config.DataDir, "data", "postmaster.pid"))
		Expect(err).NotTo(HaveOccurred())
		Expect(postmasterAfter).To(Equal(postmasterBefore))

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, reusedDSN)
		Expect(err).NotTo(HaveOccurred())
		defer conn.Close(context.Background()) //nolint:errcheck

		var dataDirectory, fsync, synchronousCommit, fullPageWrites string
		var maxConnections int
		Expect(conn.QueryRow(ctx, "SHOW data_directory").Scan(&dataDirectory)).To(Succeed())
		Expect(conn.QueryRow(ctx, "SHOW fsync").Scan(&fsync)).To(Succeed())
		Expect(conn.QueryRow(ctx, "SHOW synchronous_commit").Scan(&synchronousCommit)).To(Succeed())
		Expect(conn.QueryRow(ctx, "SHOW full_page_writes").Scan(&fullPageWrites)).To(Succeed())
		Expect(conn.QueryRow(ctx, "SHOW max_connections").Scan(&maxConnections)).To(Succeed())

		expectedDataDirectory, err := filepath.EvalSymlinks(filepath.Join(config.DataDir, "data"))
		Expect(err).NotTo(HaveOccurred())
		actualDataDirectory, err := filepath.EvalSymlinks(dataDirectory)
		Expect(err).NotTo(HaveOccurred())
		Expect(actualDataDirectory).To(Equal(expectedDataDirectory))
		Expect(validateFastTestingSettings(
			fsync,
			synchronousCommit,
			fullPageWrites,
			maxConnections,
		)).To(Succeed())
	})
})
