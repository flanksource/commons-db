package sampleframe_test

import (
	"github.com/flanksource/commons-db/recordstore/sampleframe"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("weighted stacks", func() {
	It("preserves fractional process usage and owners with the same label", func() {
		root, err := sampleframe.BuildHierarchy([]sampleframe.WeightedStack{{Frames: []string{"worker", "process"}, IDs: []string{"owner-a", "process-a"}, Value: 1.25}, {Frames: []string{"worker", "process"}, IDs: []string{"owner-b", "process-b"}, Value: 2.75}})
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Total).To(Equal(float64(4)))
		Expect(root.Children).To(HaveLen(2))
		Expect(root.Children[0].ID).NotTo(Equal(root.Children[1].ID))
	})
	It("conserves exclusive totals and distinguishes recursion and separate paths", func() {
		root, err := sampleframe.BuildHierarchy([]sampleframe.WeightedStack{{Frames: []string{"main", "work", "work"}, Value: 3}, {Frames: []string{"main", "work"}, Value: 2}, {Frames: []string{"other", "work"}, Value: 7}})
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Total).To(Equal(float64(12)))
		work := root.Children[0].Children[0]
		Expect(work.Total).To(Equal(float64(5)))
		Expect(work.Self).To(Equal(float64(2)))
		Expect(work.Children[0].Self).To(Equal(float64(3)))
		Expect(work.Children[0].ID).NotTo(Equal(work.ID))
		Expect(root.Children[1].Children[0].ID).NotTo(Equal(work.ID))
	})
})
