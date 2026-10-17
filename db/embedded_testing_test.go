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
		config := EmbeddedConfig{DataDir: "/srv/postgres"}

		applyEmbeddedDefaults(&config)

		Expect(config).To(Equal(EmbeddedConfig{
			DataDir:     "/srv/postgres",
			BinariesDir: "/srv/postgres/bin",
			Database:    "postgres",
			Username:    "postgres",
			Password:    "postgres",
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

	It("combines durability, host-scaled and performance-diagnostic settings", func() {
		scaled := map[string]string{"max_connections": "600", "jit": "off"}

		Expect(embeddedStartParameters(EmbeddedConfig{
			FastTesting:            true,
			PerformanceDiagnostics: true,
		}, scaled)).To(Equal(map[string]string{
			"fsync":                    "off",
			"synchronous_commit":       "off",
			"full_page_writes":         "off",
			"max_connections":          "600",
			"jit":                      "off",
			"shared_preload_libraries": "pg_stat_statements",
			"track_io_timing":          "on",
		}))
	})

	It("returns no settings when neither mode is enabled", func() {
		Expect(embeddedStartParameters(EmbeddedConfig{}, nil)).To(BeEmpty())
	})

	It("accepts a server with durability off", func() {
		Expect(validateFastTestingSettings("off", "off", "off")).To(Succeed())
	})

	DescribeTable("rejects a durable server",
		func(fsync, synchronousCommit, fullPageWrites string, expected string) {
			Expect(validateFastTestingSettings(fsync, synchronousCommit, fullPageWrites)).
				To(MatchError(ContainSubstring(expected)))
		},
		Entry("fsync enabled", "on", "off", "off", "fsync=off"),
		Entry("synchronous commit enabled", "off", "on", "off", "synchronous_commit=off"),
		Entry("full-page writes enabled", "off", "off", "on", "full_page_writes=off"),
	)

	Describe("drift between a running server and the host-scaled settings", func() {
		want := map[string]string{
			"max_connections":    "600",
			"shared_buffers":     "2048MB",
			"checkpoint_timeout": "1d",
			"max_wal_size":       "8192MB",
			"wal_level":          "minimal",
		}
		current := map[string]serverSetting{
			"max_connections":    {Setting: "600", Display: "600"},
			"shared_buffers":     {Setting: "262144", Unit: "8kB", Display: "2GB"},
			"checkpoint_timeout": {Setting: "86400", Unit: "s", Display: "1d"},
			"max_wal_size":       {Setting: "16384", Unit: "MB", Display: "16GB"},
			"wal_level":          {Setting: "minimal", Display: "minimal"},
		}
		with := func(name string, setting serverSetting) map[string]serverSetting {
			have := make(map[string]serverSetting, len(current))
			for key, value := range current {
				have[key] = value
			}
			have[name] = setting
			return have
		}

		It("reports nothing when every setting meets or exceeds the computed value", func() {
			Expect(testingSettingDrift(want, current)).To(BeEmpty())
		})

		It("lists each lower or different setting with have and want", func() {
			have := with("shared_buffers", serverSetting{Setting: "16384", Unit: "8kB", Display: "128MB"})
			have["max_connections"] = serverSetting{Setting: "200", Display: "200"}
			have["wal_level"] = serverSetting{Setting: "replica", Display: "replica"}

			Expect(testingSettingDrift(want, have)).To(Equal([]string{
				"max_connections: have 200, want 600",
				"shared_buffers: have 128MB, want 2048MB",
				"wal_level: have replica, want minimal",
			}))
		})

		It("fails when the server does not report a computed setting", func() {
			have := with("max_connections", serverSetting{})
			delete(have, "max_connections")

			_, err := testingSettingDrift(want, have)
			Expect(err).To(MatchError(ContainSubstring("max_connections")))
		})

		It("fails when the server reports a unit it cannot compare", func() {
			_, err := testingSettingDrift(want, with("shared_buffers", serverSetting{Setting: "1", Unit: "parsec"}))
			Expect(err).To(MatchError(ContainSubstring("parsec")))
		})
	})
})
