package patch_test

import (
	"context"
	"testing"

	"github.com/flanksource/commons-db/git/patch"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPatch(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Git patches") }

var _ = Describe("Patch workspaces", func() {
	It("round trips binary, empty, deleted and unterminated files", func() {
		before := map[string][]byte{"rules/change": []byte("old\r\n"), "rules/delete": []byte("gone")}
		after := map[string][]byte{"rules/change": []byte("new\r\n"), "rules/empty": {}, "rules/binary": {0, 1, 255}}
		directory := GinkgoT().TempDir()
		diff, err := patch.Create(context.Background(), patch.CreateOptions{Directory: directory, Before: before, After: after})
		Expect(err).NotTo(HaveOccurred())
		merged, err := patch.Merge(context.Background(), patch.MergeOptions{Directory: directory, Base: before, Current: before, Patch: diff})
		Expect(err).NotTo(HaveOccurred())
		Expect(merged.Files).To(Equal(after))
		Expect(merged.Conflicts).To(BeEmpty())
	})

	It("preserves diff3 conflict markers and reports cancellation", func() {
		base := map[string][]byte{"rules/change": []byte("original\n")}
		directory := GinkgoT().TempDir()
		diff, err := patch.Create(context.Background(), patch.CreateOptions{Directory: directory, Before: base, After: map[string][]byte{"rules/change": []byte("package\n")}})
		Expect(err).NotTo(HaveOccurred())
		merged, err := patch.Merge(context.Background(), patch.MergeOptions{Directory: directory, Base: base, Current: map[string][]byte{"rules/change": []byte("destination\n")}, Patch: diff})
		Expect(err).NotTo(HaveOccurred())
		Expect(merged.Conflicts).To(Equal([]string{"rules/change"}))
		Expect(string(merged.Files["rules/change"])).To(ContainSubstring("||||||| base\noriginal\n"))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = patch.Create(ctx, patch.CreateOptions{Directory: directory, Before: base, After: base})
		Expect(err).To(MatchError(context.Canceled))
	})
})
