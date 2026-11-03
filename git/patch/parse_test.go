package patch_test

import (
	"github.com/flanksource/commons-db/git/patch"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Parsed patch files", func() {
	It("reverses and reapplies without changing the original parsed file", func(ctx SpecContext) {
		before, after := []byte("old\n"), []byte("new\n")
		raw, err := patch.Create(ctx, patch.CreateOptions{Directory: GinkgoT().TempDir(), Before: map[string][]byte{"rule.xml": before}, After: map[string][]byte{"rule.xml": after}})
		Expect(err).NotTo(HaveOccurred())
		files, err := patch.Parse(ctx, []byte(raw))
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(HaveLen(1))
		original := files[0].String()
		reverse, err := files[0].Reverse(ctx)
		Expect(err).NotTo(HaveOccurred())
		restored, err := reverse.Apply(ctx, after)
		Expect(err).NotTo(HaveOccurred())
		Expect(restored).To(Equal(before))
		Expect(files[0].String()).To(Equal(original))
		updated, err := files[0].Apply(ctx, before)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated).To(Equal(after))
	})
})
