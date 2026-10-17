package dbtest

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("macOS RAM disk", func() {
	const hdiutilInfo = "framework       : 683.100.3\n" +
		"driver          : 683.100.3\n" +
		"================================================\n" +
		"image-path      : /Users/someone/disk.dmg\n" +
		"blockcount      : 1000\n" +
		"blocksize       : 512\n" +
		"/dev/disk4\t\t\n" +
		"/dev/disk4s1\t41504653-0000-11AA-AA11-00306543ECAC\t/Volumes/commons-db\n" +
		"================================================\n" +
		"image-path      : ram://262144\n" +
		"blockcount      : 262144\n" +
		"blocksize       : 512\n" +
		"/dev/disk5\t\t\n" +
		"/dev/disk6\tEF57347C-0000-11AA-AA11-00306543ECAC\t\n" +
		"/dev/disk6s1\t41504653-0000-11AA-AA11-00306543ECAC\t/Volumes/commons-db-ram\n"

	It("finds the RAM-backed image mounted at a volume", func() {
		Expect(ramDiskImageAt(hdiutilInfo, "/Volumes/commons-db-ram")).To(Equal(&ramDiskImage{
			Device: "/dev/disk5",
			Bytes:  262144 * 512,
		}))
	})

	It("does not mistake a file-backed image for a RAM disk", func() {
		image, err := ramDiskImageAt(hdiutilInfo, "/Volumes/commons-db")
		Expect(err).NotTo(HaveOccurred())
		Expect(image).To(BeNil())
	})

	It("creates a RAM disk volume, reuses it, and detaches it", Label("integration"), func() {
		if os.Getenv("COMMONS_DB_RAMDISK_TEST") == "" {
			Skip("set COMMONS_DB_RAMDISK_TEST=1 to attach a RAM disk (needs DiskImages access outside a sandbox)")
		}
		const size = uint64(128 << 20)
		name := fmt.Sprintf("commons-db-test-%d", time.Now().UnixNano())
		lockDir := GinkgoT().TempDir()

		mountPoint, err := ramBackedDataDir(name, size, lockDir)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(detachRAMDisk(mountPoint)).To(Succeed()) })
		Expect(mountPoint).To(Equal(filepath.Join("/Volumes", name)))
		Expect(os.WriteFile(filepath.Join(mountPoint, "marker"), []byte("kept"), 0o600)).To(Succeed())

		reused, err := ramBackedDataDir(name, size, lockDir)
		Expect(err).NotTo(HaveOccurred())
		Expect(reused).To(Equal(mountPoint))
		Expect(os.ReadFile(filepath.Join(reused, "marker"))).To(Equal([]byte("kept")))

		_, err = ramBackedDataDir(name, 2*size, lockDir)
		Expect(err).To(MatchError(ContainSubstring("smaller than")))
	})
})
