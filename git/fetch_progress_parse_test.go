package git

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("fetch progress parser", func() {
	DescribeTable("reads a git progress line", func(line string, expected FetchProgress) {
		progress, ok := parseFetchProgress(line)
		Expect(ok).To(BeTrue())
		Expect(progress).To(Equal(expected))
	},
		Entry("remote counting", "remote: Counting objects:  50% (5/10)",
			FetchProgress{Phase: "Counting objects", Remote: true, Percent: 50, Current: 5, Total: 10}),
		Entry("remote phase done", "remote: Compressing objects: 100% (3/3), done.",
			FetchProgress{Phase: "Compressing objects", Remote: true, Percent: 100, Current: 3, Total: 3, PhaseDone: true}),
		Entry("receiving with throughput", "Receiving objects:  45% (450/1000), 12.00 MiB | 3.00 MiB/s",
			FetchProgress{Phase: "Receiving objects", Percent: 45, Current: 450, Total: 1000, Transferred: "12.00 MiB", Rate: "3.00 MiB/s"}),
		Entry("receiving done with throughput", "Receiving objects: 100% (1000/1000), 26.50 MiB | 4.10 MiB/s, done.",
			FetchProgress{Phase: "Receiving objects", Percent: 100, Current: 1000, Total: 1000, Transferred: "26.50 MiB", Rate: "4.10 MiB/s", PhaseDone: true}),
		Entry("resolving deltas", "Resolving deltas: 100% (3/3), done.",
			FetchProgress{Phase: "Resolving deltas", Percent: 100, Current: 3, Total: 3, PhaseDone: true}),
		Entry("unbounded enumeration", "remote: Enumerating objects: 12, done.",
			FetchProgress{Phase: "Enumerating objects", Remote: true, Current: 12, PhaseDone: true}),
	)

	DescribeTable("ignores lines that are not progress", func(line string) {
		_, ok := parseFetchProgress(line)
		Expect(ok).To(BeFalse())
	},
		Entry("remote summary", "remote: Total 10 (delta 2), reused 0 (delta 0), pack-reused 0"),
		Entry("ref update", " * branch            main       -> FETCH_HEAD"),
		Entry("fatal error", "fatal: repository 'x' not found"),
		Entry("empty", ""),
	)

	DescribeTable("reads git's transferred size in KiB", func(transferred string, expected int64, known bool) {
		kib, ok := transferredKiB(transferred)
		Expect([]any{kib, ok}).To(Equal([]any{expected, known}))
	},
		Entry("bytes", "512 bytes", int64(0), true),
		Entry("KiB", "12.50 KiB", int64(12), true),
		Entry("MiB", "104.33 MiB", int64(106833), true),
		Entry("GiB", "2.00 GiB", int64(2097152), true),
		Entry("absent", "", int64(0), false),
		Entry("unknown unit", "12 parsecs", int64(0), false),
	)

	It("splits carriage-return redraws, reports progress and keeps other output for errors", func() {
		var seen []FetchProgress
		writer := &progressWriter{onProgress: func(progress FetchProgress) { seen = append(seen, progress) }}
		_, err := writer.Write([]byte("Receiving objects:  50% (1/2)\rReceiving obj"))
		Expect(err).NotTo(HaveOccurred())
		_, err = writer.Write([]byte("ects: 100% (2/2), done.\nfatal: something broke\n"))
		Expect(err).NotTo(HaveOccurred())
		writer.flush()
		Expect(seen).To(Equal([]FetchProgress{
			{Phase: "Receiving objects", Percent: 50, Current: 1, Total: 2},
			{Phase: "Receiving objects", Percent: 100, Current: 2, Total: 2, PhaseDone: true},
		}))
		Expect(writer.other.String()).To(Equal("fatal: something broke\n"))
	})
})
