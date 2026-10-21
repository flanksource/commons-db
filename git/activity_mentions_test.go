package git_test

import (
	"github.com/flanksource/commons-db/git"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PR activity mentions", func() {
	DescribeTable("extracts provider mention identities from prose", func(body string, expected []string) {
		Expect(git.ActivityMentions(body)).To(Equal(expected))
	},
		Entry("users and groups", "Please ask @Reviewer and @acme/review-team; @reviewer already saw this.", []string{"reviewer", "acme/review-team"}),
		Entry("email and inline code", "Contact grace@example.com or use `@example` to document the syntax.", []string{}),
		Entry("fenced code", "```text\n@example\n```\nAsk @reviewer instead.", []string{"reviewer"}),
		Entry("native identity", "Please ask @<reviewer-identity>.", []string{"reviewer-identity"}),
		Entry("native mention anchor", `<a data-vss-mention="version:2.0,id:reviewer-identity">Reviewer</a>`, []string{"reviewer-identity"}),
	)
})
