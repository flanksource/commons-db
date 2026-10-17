package dbtest

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Linux /dev/shm data directory", func() {
	It("uses /dev/shm when it holds the required size", func() {
		Expect(ramBackedDataDir("commons-db", 1<<20, "")).To(Equal("/dev/shm/commons-db"))
	})

	It("rejects /dev/shm when the required size exceeds it", func() {
		_, err := ramBackedDataDir("commons-db", 1<<50, "")
		Expect(err).To(MatchError(ContainSubstring("--shm-size")))
	})
})
