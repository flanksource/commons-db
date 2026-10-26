package git_test

import (
	"os"
	"strings"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Cache.ReadBlobs", func() {
	It("batches literal paths across commits and preserves binary, empty and missing blobs", func(ctx SpecContext) {
		fixture := newBareRepo("blobs")
		const path = "[Plan]\trule\n.xml"
		const original = "\x00binary\n\xff"
		old := fixture.commitOn(fixtureCommit{Branch: "main", Path: path, Content: original, Message: "Add binary"})
		head := fixture.commitOn(fixtureCommit{Branch: "main", Path: path, Message: "Empty binary"})
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		repo := mirroredRepository(fixture)
		log := argvRecorder()
		blobs, err := cache.ReadBlobs(ctx, repo, []git.BlobRequest{{SHA: old, Path: path}, {SHA: head, Path: path}, {SHA: head, Path: "absent.xml"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(blobs).To(HaveLen(3))
		Expect([]any{string(blobs[0].Content), blobs[0].Present, len(blobs[1].Content), blobs[1].Present, blobs[2].Present}).To(Equal(
			[]any{original, true, 0, true, false}))
		Expect(blobs[0].Object).NotTo(BeEmpty())
		commands, err := os.ReadFile(log)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(string(commands), "cat-file --batch")).To(Equal(1))
		Expect(string(commands)).NotTo(ContainSubstring("ls-tree"))
		Expect(string(commands)).NotTo(ContainSubstring("worktree"))
	})
})
