package git

import (
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("blame porcelain parser", func() {
	const sha = "1111111111111111111111111111111111111111"
	const metadata = "author Ada Example\nauthor-mail <ada@example.test>\nauthor-time 1700000000\nauthor-tz +0000\nsummary Add rule\nfilename \"rules/Plan\\tOne.xml\"\n"
	It("decodes metadata and quoted filenames without trimming content", func() {
		lines, err := parseBlame([]byte(sha + " 2 1 1\n" + metadata + "\t  <Rule/>  \r\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(HaveLen(1))
		Expect([]any{lines[0].Line, lines[0].OriginalLine, lines[0].Path, lines[0].Content, lines[0].Commit.Author, lines[0].Commit.Date.Unix()}).To(Equal(
			[]any{1, 2, "rules/Plan\tOne.xml", "  <Rule/>  \r", BlameAuthor{Name: "Ada Example", Email: "ada@example.test"}, int64(1700000000)}))
		author, err := json.Marshal(lines[0].Commit.Author)
		Expect(err).NotTo(HaveOccurred())
		Expect(author).To(MatchJSON(`{"name":"Ada Example","email":"ada@example.test"}`))
	})
	DescribeTable("refuses malformed porcelain", func(raw string) {
		_, err := parseBlame([]byte(raw))
		Expect(err).To(HaveOccurred())
	},
		Entry("invalid header", "not-a-sha 1 1\n"),
		Entry("invalid line number", sha+" zero 1\n"),
		Entry("missing author", sha+" 1 1\nfilename rule.xml\n\t<Rule/>\n"),
		Entry("invalid timestamp", sha+" 1 1\n"+strings.Replace(metadata, "1700000000", "never", 1)+"\t<Rule/>\n"),
		Entry("truncated metadata", sha+" 1 1\n"+metadata),
	)
})
