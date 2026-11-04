package git

import (
	"bufio"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"io"
	"strings"
)

var _ = Describe("Object batch framing", func() {
	DescribeTable("validates complete object contents and terminators", func(payload string, valid bool) {
		err := readBatchObject(bufio.NewReader(strings.NewReader(payload)), "abc", func(_ batchBlob, r io.Reader) error { _, err := io.ReadAll(r); return err })
		if valid {
			Expect(err).NotTo(HaveOccurred())
		} else {
			Expect(err).To(HaveOccurred())
		}
	},
		Entry("binary blob", "abc blob 4\x00\x00abc\x00", true),
		Entry("empty blob", "abc blob 0\x00\x00", true),
		Entry("truncated content", "abc blob 4\x00ab", false),
		Entry("wrong terminator", "abc blob 4\x00abcd\n", false),
		Entry("wrong object", "def blob 4\x00abcd\x00", false),
	)
})
