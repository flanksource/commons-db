package db

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("host-scaled test PostgreSQL settings", func() {
	const gib = uint64(1 << 30)

	scaled := func(maxConnections, sharedBuffers, maxWALSize string) map[string]string {
		return map[string]string{
			"max_connections":           maxConnections,
			"shared_buffers":            sharedBuffers,
			"max_locks_per_transaction": "256",
			"maintenance_work_mem":      "256MB",
			"work_mem":                  "8MB",
			"jit":                       "off",
			"wal_level":                 "minimal",
			"max_wal_senders":           "0",
			"checkpoint_timeout":        "1d",
			"max_wal_size":              maxWALSize,
			"wal_buffers":               "64MB",
		}
	}

	DescribeTable("sizes the server from the host",
		func(limits HostLimits, ramDiskBytes uint64, expected map[string]string) {
			Expect(ScaledTestingParameters(limits, ramDiskBytes)).To(Equal(expected))
		},
		Entry("a dedicated 4 CPU / 16GB CI runner gives a quarter of memory to shared buffers",
			HostLimits{CPUs: 4, MemoryBytes: 16 * gib, CI: true}, uint64(0),
			scaled("200", "4096MB", "8192MB")),
		Entry("a 16 CPU / 64GB workstation caps shared buffers at 2GB",
			HostLimits{CPUs: 16, MemoryBytes: 64 * gib}, uint64(0),
			scaled("800", "2048MB", "8192MB")),
		Entry("a 2 CPU / 1GB container keeps the floors",
			HostLimits{CPUs: 2, MemoryBytes: 1 * gib}, uint64(0),
			scaled("200", "128MB", "8192MB")),
		Entry("a 32 CPU host caps connections and a 2GB RAM disk caps WAL at a quarter",
			HostLimits{CPUs: 32, MemoryBytes: 32 * gib}, 2*gib,
			scaled("1000", "2048MB", "512MB")),
	)

	Describe("Linux memory detection", func() {
		const meminfo = "MemTotal:       16384000 kB\nMemFree:         1000000 kB\n"
		var dir string

		write := func(name, content string) string {
			path := filepath.Join(dir, name)
			Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
			return path
		}

		BeforeEach(func() { dir = GinkgoT().TempDir() })

		DescribeTable("takes the smaller of the cgroup limit and MemTotal",
			func(v2, v1 string, expected uint64) {
				v2Path, v1Path := filepath.Join(dir, "missing-v2"), filepath.Join(dir, "missing-v1")
				if v2 != "" {
					v2Path = write("memory.max", v2)
				}
				if v1 != "" {
					v1Path = write("memory.limit_in_bytes", v1)
				}
				memory, err := linuxMemoryBytes(v2Path, v1Path, write("meminfo", meminfo))
				Expect(err).NotTo(HaveOccurred())
				Expect(memory).To(Equal(expected))
			},
			Entry("cgroup v2 unlimited", "max\n", "", uint64(16384000*1024)),
			Entry("cgroup v2 numeric limit", "2147483648\n", "", uint64(2147483648)),
			Entry("missing v2 falls through to the v1 limit", "", "1073741824\n", uint64(1073741824)),
			Entry("cgroup v1 unlimited sentinel is above MemTotal", "", "9223372036854771712\n", uint64(16384000*1024)),
			Entry("no cgroup memory controller", "", "", uint64(16384000*1024)),
		)

		It("fails with the path when the cgroup limit is unparseable", func() {
			v2Path := write("memory.max", "lots\n")
			_, err := linuxMemoryBytes(v2Path, filepath.Join(dir, "missing-v1"), write("meminfo", meminfo))
			Expect(err).To(MatchError(ContainSubstring(v2Path)))
		})

		It("fails with the path when meminfo has no MemTotal", func() {
			meminfoPath := write("meminfo", "MemFree: 1 kB\n")
			_, err := linuxMemoryBytes(filepath.Join(dir, "a"), filepath.Join(dir, "b"), meminfoPath)
			Expect(err).To(MatchError(ContainSubstring(meminfoPath)))
		})
	})
})
