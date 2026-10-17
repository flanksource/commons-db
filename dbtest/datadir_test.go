package dbtest

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("embedded data directory resolution", func() {
	const (
		optionDir = "/srv/option"
		envDir    = "/srv/env"
		binDir    = "/srv/bin"
		gib       = uint64(1 << 30)
	)
	defaultRoot := filepath.Join(os.TempDir(), "commons-db")

	DescribeTable("resolves the cluster and binary directories",
		func(optionDataDir, envDataDir, envBinDir string, expected embeddedDirs) {
			GinkgoT().Setenv(EnvDataDir, envDataDir)
			GinkgoT().Setenv(EnvBinDir, envBinDir)

			Expect(resolveEmbeddedDirs(optionDataDir)).To(Equal(expected))
		},
		Entry("defaults to $TMPDIR/commons-db", "", "", "",
			embeddedDirs{DataDir: defaultRoot, BinariesDir: filepath.Join(defaultRoot, "bin")}),
		Entry("takes a literal COMMONS_DB_DATA_DIR path", "", envDir, "",
			embeddedDirs{DataDir: envDir, BinariesDir: filepath.Join(defaultRoot, "bin")}),
		Entry("prefers Options.DataDir over the environment", optionDir, envDir, "",
			embeddedDirs{DataDir: optionDir, BinariesDir: filepath.Join(defaultRoot, "bin")}),
		Entry("keeps binaries in COMMONS_DB_BIN_DIR", optionDir, "", binDir,
			embeddedDirs{DataDir: optionDir, BinariesDir: binDir}),
	)

	It("rejects an unparseable COMMONS_DB_TMPFS_SIZE before touching any filesystem", func() {
		GinkgoT().Setenv(EnvDataDir, tmpfsDataDir)
		GinkgoT().Setenv(EnvTmpfsSize, "lots")

		_, err := resolveEmbeddedDirs("")
		Expect(err).To(MatchError(ContainSubstring(`COMMONS_DB_TMPFS_SIZE "lots"`)))
	})

	DescribeTable("parses byte sizes in binary units",
		func(value string, expected uint64) {
			Expect(parseByteSize(value)).To(Equal(expected))
		},
		Entry("gigabytes", "2GB", 2*gib),
		Entry("short suffix", "512m", 512*uint64(1<<20)),
		Entry("IEC suffix", "1GiB", gib),
		Entry("plain bytes", "1048576", uint64(1<<20)),
	)

	Describe("tmpfs validation", func() {
		const shm = "/dev/shm"

		It("accepts a tmpfs mount at least as large as required", func() {
			Expect(validateTmpfs(shm, filesystemStat{Tmpfs: true, CapacityBytes: 4 * gib}, 2*gib)).To(Succeed())
		})

		It("rejects a directory that is not on tmpfs", func() {
			err := validateTmpfs(shm, filesystemStat{Tmpfs: false, CapacityBytes: 4 * gib}, 2*gib)
			Expect(err).To(MatchError(ContainSubstring("/dev/shm is not a tmpfs mount")))
		})

		It("rejects an undersized tmpfs and says how to grow a container's", func() {
			err := validateTmpfs(shm, filesystemStat{Tmpfs: true, CapacityBytes: 64 << 20}, 2*gib)
			Expect(err).To(MatchError(And(ContainSubstring("64MB"), ContainSubstring("2048MB"), ContainSubstring("--shm-size"))))
		})
	})
})
