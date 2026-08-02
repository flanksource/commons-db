package db

import (
	"os"
	"path/filepath"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("fast embedded PostgreSQL settings", func() {
	It("applies the documented embedded defaults", func() {
		config := EmbeddedConfig{}

		applyEmbeddedDefaults(&config)

		Expect(config).To(Equal(EmbeddedConfig{
			Database: "postgres",
			Username: "postgres",
			Password: "postgres",
		}))
	})

	It("defaults a fresh data directory to PostgreSQL 16", func() {
		version, err := detectPGVersion(GinkgoT().TempDir())

		Expect(err).NotTo(HaveOccurred())
		Expect(version).To(Equal(embeddedpostgres.V16))
	})

	It("uses the major version recorded by a persistent cluster", func() {
		dataPath := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dataPath, "PG_VERSION"), []byte("17\n"), 0o600)).To(Succeed())

		version, err := detectPGVersion(dataPath)

		Expect(err).NotTo(HaveOccurred())
		Expect(version).To(Equal(embeddedpostgres.V17))
	})

	It("restricts the PostgreSQL data directory without changing its root", func() {
		root := filepath.Join(GinkgoT().TempDir(), "postgres")
		dataPath := filepath.Join(root, "data")
		Expect(os.MkdirAll(dataPath, 0o777)).To(Succeed())
		Expect(os.Chmod(root, 0o755)).To(Succeed())
		Expect(os.Chmod(dataPath, 0o777)).To(Succeed())

		Expect(createEmbeddedDirectories(root)).To(Succeed())

		rootInfo, err := os.Stat(root)
		Expect(err).NotTo(HaveOccurred())
		dataInfo, err := os.Stat(dataPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(rootInfo.Mode().Perm()).To(Equal(os.FileMode(0o755)))
		Expect(dataInfo.Mode().Perm()).To(Equal(os.FileMode(0o700)))
	})

	It("creates a fresh root with group access and a private data directory", func() {
		root := filepath.Join(GinkgoT().TempDir(), "postgres")

		Expect(createEmbeddedDirectories(root)).To(Succeed())

		rootInfo, err := os.Stat(root)
		Expect(err).NotTo(HaveOccurred())
		dataInfo, err := os.Stat(filepath.Join(root, "data"))
		Expect(err).NotTo(HaveOccurred())
		Expect(rootInfo.Mode().Perm()).To(Equal(os.FileMode(0o750)))
		Expect(dataInfo.Mode().Perm()).To(Equal(os.FileMode(0o700)))
	})

	It("rejects an unsupported persistent cluster version", func() {
		dataPath := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dataPath, "PG_VERSION"), []byte("19\n"), 0o600)).To(Succeed())

		_, err := detectPGVersion(dataPath)

		Expect(err).To(MatchError(ContainSubstring(`unsupported PostgreSQL data version "19"`)))
	})

	It("returns the complete disposable-test configuration when enabled", func() {
		Expect(fastTestingStartParameters(true)).To(Equal(map[string]string{
			"fsync":              "off",
			"synchronous_commit": "off",
			"full_page_writes":   "off",
			"max_connections":    "200",
		}))
	})

	It("returns no settings when disabled", func() {
		Expect(fastTestingStartParameters(false)).To(BeEmpty())
	})

	It("combines fast-test and performance-diagnostic settings", func() {
		Expect(embeddedStartParameters(EmbeddedConfig{
			FastTesting:            true,
			PerformanceDiagnostics: true,
		})).To(Equal(map[string]string{
			"fsync":                    "off",
			"synchronous_commit":       "off",
			"full_page_writes":         "off",
			"max_connections":          "200",
			"shared_preload_libraries": "pg_stat_statements",
			"track_io_timing":          "on",
		}))
	})

	It("accepts a server with the required settings", func() {
		Expect(validateFastTestingSettings("off", "off", "off", 200)).To(Succeed())
	})

	DescribeTable("rejects an unsafe or undersized server",
		func(fsync, synchronousCommit, fullPageWrites string, maxConnections int, expected string) {
			Expect(validateFastTestingSettings(
				fsync,
				synchronousCommit,
				fullPageWrites,
				maxConnections,
			)).To(MatchError(ContainSubstring(expected)))
		},
		Entry("fsync enabled", "on", "off", "off", 200, "fsync=off"),
		Entry("synchronous commit enabled", "off", "on", "off", 200, "synchronous_commit=off"),
		Entry("full-page writes enabled", "off", "off", "on", 200, "full_page_writes=off"),
		Entry("connection ceiling too low", "off", "off", "off", 199, "max_connections"),
	)
})
