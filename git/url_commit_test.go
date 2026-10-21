package git_test

import (
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RepoURL.CommitURL for Git blame", func() {
	const sha = "0123456789012345678901234567890123456789"
	DescribeTable("links immutable commits without clone credentials or SSH ports", func(raw, hint, expected string) {
		repo, err := git.ParseRepoURL(raw, hint)
		Expect(err).NotTo(HaveOccurred())
		Expect(repo.CommitURL(sha)).To(Equal(expected + sha))
	},
		Entry("GitHub", "git@github.com:acme/rules.git", "", "https://github.com/acme/rules/commit/"),
		Entry("GitLab", "ssh://git@gitlab.example.test:2222/acme/platform/rules.git", "gitlab", "https://gitlab.example.test/acme/platform/rules/-/commit/"),
		Entry("Azure HTTPS", "https://bot@dev.azure.com/acme/Platform/_git/rules", "", "https://dev.azure.com/acme/Platform/_git/rules/commit/"),
		Entry("Azure SSH", "git@ssh.dev.azure.com:v3/acme/Platform/rules", "", "https://dev.azure.com/acme/Platform/_git/rules/commit/"),
		Entry("Azure legacy", "https://acme.visualstudio.com/Platform/_git/rules", "", "https://acme.visualstudio.com/Platform/_git/rules/commit/"),
	)
	It("rejects invalid commit identifiers and reports local repositories without a web view", func() {
		repo, err := git.ParseRepoURL("https://github.com/acme/rules", "")
		Expect(err).NotTo(HaveOccurred())
		_, err = repo.CommitURL("../main?secret=example")
		Expect(err).To(MatchError(ContainSubstring("full 40-character commit SHA")))
		local, err := git.ParseRepoURL("/srv/rules", "")
		Expect(err).NotTo(HaveOccurred())
		_, err = local.CommitURL(sha)
		Expect(err).To(MatchError(git.ErrNoCommitView))
	})
})
