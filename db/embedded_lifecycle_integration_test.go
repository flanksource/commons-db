package db

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/jackc/pgx/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("embedded PostgreSQL lifecycle", Label("integration"), func() {
	const staleWorkMem = "4MB"

	BeforeEach(func() {
		if os.Getenv("COMMONS_DB_EMBEDDED_TEST") == "" {
			Skip("set COMMONS_DB_EMBEDDED_TEST=1 to run embedded PostgreSQL lifecycle tests")
		}
	})

	newConfig := func(fastTesting bool) EmbeddedConfig {
		port, err := FreePort()
		Expect(err).NotTo(HaveOccurred())
		return EmbeddedConfig{
			DataDir:     GinkgoT().TempDir(),
			Database:    "postgres",
			Port:        uint32(port), //nolint:gosec // FreePort always returns a valid TCP port
			FastTesting: fastTesting,
		}
	}

	// startStale starts a fast server the way an older release would have:
	// with a lower work_mem than this host's scaled settings ask for.
	startStale := func(config EmbeddedConfig) (string, map[string]string) {
		runtime, err := newEmbeddedRuntime(config)
		Expect(err).NotTo(HaveOccurred())
		scaled := maps.Clone(runtime.scaledTesting)
		runtime.scaledTesting["work_mem"] = staleWorkMem
		dsn, stop, err := runtime.withStartupLock(runtime.startOrReuse)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(stop()).To(Succeed()) })
		return dsn, scaled
	}

	settings := func(ctx context.Context, dsn string, names []string) map[string]serverSetting {
		conn, err := pgx.Connect(ctx, dsn)
		Expect(err).NotTo(HaveOccurred())
		defer conn.Close(context.Background()) //nolint:errcheck
		have, err := readServerSettings(ctx, conn, names)
		Expect(err).NotTo(HaveOccurred())
		return have
	}

	It("reuses a server with stale scaled settings in place, reporting the drift instead of failing", func(ctx SpecContext) {
		config := newConfig(true)
		dsn, scaled := startStale(config)
		postmasterBefore, err := os.ReadFile(filepath.Join(config.DataDir, "data", "postmaster.pid"))
		Expect(err).NotTo(HaveOccurred())

		reusedDSN, stopReuser, err := StartEmbedded(config)
		Expect(err).NotTo(HaveOccurred())
		Expect(reusedDSN).To(Equal(dsn))
		Expect(stopReuser()).To(Succeed())

		postmasterAfter, err := os.ReadFile(filepath.Join(config.DataDir, "data", "postmaster.pid"))
		Expect(err).NotTo(HaveOccurred())
		Expect(postmasterAfter).To(Equal(postmasterBefore))

		names := slices.Concat(slices.Collect(maps.Keys(scaled)), []string{"fsync", "synchronous_commit", "full_page_writes", "data_directory"})
		have := settings(ctx, dsn, names)
		expectedDataDirectory, err := filepath.EvalSymlinks(filepath.Join(config.DataDir, "data"))
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.EvalSymlinks(have["data_directory"].Setting)).To(Equal(expectedDataDirectory))
		Expect(validateFastTestingSettings(have["fsync"].Setting, have["synchronous_commit"].Setting, have["full_page_writes"].Setting)).To(Succeed())
		Expect(testingSettingDrift(scaled, have)).To(Equal([]string{"work_mem: have " + staleWorkMem + ", want 8MB"}))
	})

	It("reuses a fast server on the port even when it was started from another data directory", func() {
		config := newConfig(true)
		dsn, stop, err := StartEmbedded(config)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(stop()).To(Succeed()) })

		config.DataDir = GinkgoT().TempDir()
		reusedDSN, stopReuser, err := StartEmbedded(config)
		Expect(err).NotTo(HaveOccurred())
		Expect(reusedDSN).To(Equal(dsn))
		Expect(stopReuser()).To(Succeed())
		Expect(filepath.Join(config.DataDir, "data", "postmaster.pid")).NotTo(BeAnExistingFile())
	})

	It("refuses to reuse a durable server started from another data directory", func() {
		config := newConfig(false)
		_, stop, err := StartEmbedded(config)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(stop()).To(Succeed()) })

		config.DataDir = GinkgoT().TempDir()
		_, _, err = StartEmbedded(config)
		Expect(err).To(MatchError(ContainSubstring("expected PostgreSQL data_directory")))
	})

	It("refuses to reuse a durable server as a fast one", func() {
		config := newConfig(false)
		_, stop, err := StartEmbedded(config)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(stop()).To(Succeed()) })

		config.FastTesting = true
		_, _, err = StartEmbedded(config)
		Expect(err).To(MatchError(ContainSubstring("fsync=off")))
	})
})
