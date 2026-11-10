package cache

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRegistry(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Cache registry")
}

var _ = Describe("cache binding confinement", func() {
	It("rejects a binding whose owner symlink escapes the cache root", func() {
		root, outside := GinkgoT().TempDir(), GinkgoT().TempDir()
		Expect(os.MkdirAll(filepath.Join(outside, "mirror.git"), 0700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(outside, "mirror.git", "HEAD"), []byte("ref: refs/heads/main\n"), 0600)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(root, "owner"))).To(Succeed())
		Expect(Save(root, "dependent", Binding{Owner: "owner"})).To(Succeed())
		_, err := Load(root, "dependent")
		Expect(err).To(HaveOccurred())
		Expect(err).NotTo(MatchError(ErrOwnerMissing))
	})

	DescribeTable("rejects invalid cache keys before reading a binding", func(key string) {
		_, err := Load(GinkgoT().TempDir(), key)
		Expect(err).To(MatchError(ContainSubstring("invalid cache key")))
	}, Entry("empty", ""), Entry("root", "."), Entry("parent", ".."), Entry("nested", "nested/key"), Entry("absolute", "/outside"))
})
