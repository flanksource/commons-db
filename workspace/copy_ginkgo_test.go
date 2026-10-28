package workspace

import (
	"errors"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("copy-on-write copies", func() {
	// On a reflink-capable filesystem (APFS, btrfs, XFS) cloneFile must produce
	// an independent copy; anywhere else it must report errCloneUnsupported so
	// copyPath falls back to a byte copy rather than failing.
	It("clones a file independently of its source", func() {
		dir := GinkgoT().TempDir()
		write(dir, "src.txt", "original\n")
		err := cloneFile(filepath.Join(dir, "src.txt"), filepath.Join(dir, "dst.txt"))
		if errors.Is(err, errCloneUnsupported) {
			Skip("filesystem has no copy-on-write clone")
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(read(dir, "dst.txt")).To(Equal("original\n"))
		write(dir, "dst.txt", "rewritten\n")
		Expect(read(dir, "src.txt")).To(Equal("original\n"), "clone shares storage with its source")
	})

	It("copies into missing directories and reports the size", func() {
		dir := GinkgoT().TempDir()
		write(dir, "src.txt", "hello world")
		n, _, err := copyPath(filepath.Join(dir, "src.txt"), filepath.Join(dir, "nested", "dst.txt"))
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(BeEquivalentTo(len("hello world")))
		Expect(read(dir, "nested/dst.txt")).To(Equal("hello world"))
	})
})
