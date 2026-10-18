package git_test

import (
	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ParseRepoURL", func() {
	depth := func(n int) *int { return &n }

	DescribeTable("parses provider coordinates",
		func(raw, hint string, expected git.RepoURL) {
			parsed, err := git.ParseRepoURL(raw, hint)
			Expect(err).ToNot(HaveOccurred())
			Expect(parsed).To(Equal(expected))
		},
		Entry("GitHub HTTPS", "https://github.com/acme/widgets.git", "", git.RepoURL{
			Clone: "https://github.com/acme/widgets.git", Scheme: "https", Host: "github.com", Path: "acme/widgets.git",
			Provider: models.ConnectionTypeGithub, Owner: "acme", Name: "widgets"}),
		Entry("GitHub scp with ref and depth", "git@github.com:acme/widgets.git?ref=v1&depth=5", "", git.RepoURL{
			Clone: "git@github.com:acme/widgets.git", Scheme: "ssh", Host: "github.com", Path: "acme/widgets.git", User: "git",
			Provider: models.ConnectionTypeGithub, Owner: "acme", Name: "widgets", Ref: "v1", Depth: depth(5)}),
		Entry("GitLab nested groups", "https://gitlab.example.com/acme/platform/tools/cli.git", models.ConnectionTypeGitlab, git.RepoURL{
			Clone: "https://gitlab.example.com/acme/platform/tools/cli.git", Scheme: "https", Host: "gitlab.example.com",
			Path: "acme/platform/tools/cli.git", Provider: models.ConnectionTypeGitlab, Owner: "acme/platform/tools", Name: "cli"}),
		Entry("Azure dev.azure.com with user", "https://acme@dev.azure.com/acme/Platform/_git/rules", "", git.RepoURL{
			Clone: "https://acme@dev.azure.com/acme/Platform/_git/rules", Scheme: "https", Host: "dev.azure.com", User: "acme",
			Path: "acme/Platform/_git/rules", Provider: models.ConnectionTypeAzureDevops, Owner: "acme", Project: "Platform", Name: "rules"}),
		Entry("Azure dev.azure.com without user", "https://dev.azure.com/acme/Platform/_git/rules?ref=main", "", git.RepoURL{
			Clone: "https://dev.azure.com/acme/Platform/_git/rules", Scheme: "https", Host: "dev.azure.com",
			Path: "acme/Platform/_git/rules", Provider: models.ConnectionTypeAzureDevops, Owner: "acme", Project: "Platform",
			Name: "rules", Ref: "main"}),
		Entry("Azure visualstudio.com DefaultCollection", "https://acme.visualstudio.com/DefaultCollection/Platform/_git/rules", "", git.RepoURL{
			Clone: "https://acme.visualstudio.com/DefaultCollection/Platform/_git/rules", Scheme: "https", Host: "acme.visualstudio.com",
			Path: "DefaultCollection/Platform/_git/rules", Provider: models.ConnectionTypeAzureDevops, Owner: "acme",
			Project: "Platform", Name: "rules"}),
		Entry("Azure visualstudio.com", "https://acme.visualstudio.com/Platform/_git/rules", "", git.RepoURL{
			Clone: "https://acme.visualstudio.com/Platform/_git/rules", Scheme: "https", Host: "acme.visualstudio.com",
			Path: "Platform/_git/rules", Provider: models.ConnectionTypeAzureDevops, Owner: "acme", Project: "Platform", Name: "rules"}),
		Entry("Azure ssh v3", "git@ssh.dev.azure.com:v3/acme/Platform/rules", "", git.RepoURL{
			Clone: "git@ssh.dev.azure.com:v3/acme/Platform/rules", Scheme: "ssh", Host: "ssh.dev.azure.com", User: "git",
			Path: "v3/acme/Platform/rules", Provider: models.ConnectionTypeAzureDevops, Owner: "acme", Project: "Platform", Name: "rules"}),
		Entry("generic file URL", "file:///srv/git/rules.git", "", git.RepoURL{
			Clone: "file:///srv/git/rules.git", Scheme: "file", Path: "/srv/git/rules.git",
			Provider: models.ConnectionTypeGit, Owner: "srv/git", Name: "rules"}),
		Entry("generic local path", "/srv/git/rules.git", "", git.RepoURL{
			Clone: "/srv/git/rules.git", Scheme: "file", Path: "/srv/git/rules.git",
			Provider: models.ConnectionTypeGit, Owner: "srv/git", Name: "rules"}),
	)

	DescribeTable("rejects invalid URLs",
		func(raw, hint, message string) {
			_, err := git.ParseRepoURL(raw, hint)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("empty", "", "", "repository URL is required"),
		Entry("bad depth", "https://github.com/acme/widgets?depth=deep", "", "invalid depth"),
		Entry("negative depth", "https://github.com/acme/widgets?depth=-1", "", "invalid depth"),
		Entry("embedded password", "https://user:token@github.com/acme/widgets", "", "must not embed a password"),
		Entry("GitHub nested path", "https://github.com/acme/widgets/extra", "", "owner/name"),
		Entry("Azure without _git", "https://dev.azure.com/acme/Platform/rules", "", "_git"),
		Entry("unknown provider hint", "https://example.com/a/b", "bitbucket", "unsupported git provider"),
		Entry("unsupported scheme", "ftp://example.com/a/b", "", "unsupported repository URL scheme"),
		Entry("unknown query parameter", "https://github.com/acme/widgets?token=x", "", "unsupported query parameter"),
	)

	It("derives display, short and cache-key forms without user info", func() {
		parsed, err := git.ParseRepoURL("https://acme@dev.azure.com/acme/Platform/_git/rules", "")
		Expect(err).ToNot(HaveOccurred())
		Expect(parsed.Redacted()).To(Equal("https://dev.azure.com/acme/Platform/_git/rules"))
		Expect(parsed.Short()).To(Equal("dev.azure.com/acme/Platform/_git/rules"))
		Expect(parsed.CacheKey()).To(MatchRegexp(`^dev\.azure\.com_acme_Platform__git_rules-[0-9a-f]{12}$`))
	})

	It("redacts user info from URLs embedded in text", func() {
		Expect(git.Redact("fatal: unable to access 'https://user:token@example.com/repo.git/': 401")).
			To(Equal("fatal: unable to access 'https://example.com/repo.git/': 401"))
	})
})

var _ = Describe("ref names", func() {
	DescribeTable("validate",
		func(validate func(string) error, value string, valid bool) {
			if valid {
				Expect(validate(value)).To(Succeed())
			} else {
				Expect(validate(value)).To(HaveOccurred())
			}
		},
		Entry("revision", git.ValidateRevision, "origin/main~2", true),
		Entry("revision option", git.ValidateRevision, "--upload-pack=evil", false),
		Entry("revision whitespace", git.ValidateRevision, "main branch", false),
		Entry("branch nested", git.ValidateBranchName, "feature/apply-1", true),
		Entry("branch dotdot", git.ValidateBranchName, "a..b", false),
		Entry("branch lock", git.ValidateBranchName, "a/b.lock", false),
		Entry("branch HEAD", git.ValidateBranchName, "HEAD", false),
		Entry("tag", git.ValidateTagName, "v1.2.3", true),
		Entry("tag option", git.ValidateTagName, "-v1", false),
		Entry("tag colon", git.ValidateTagName, "v1:2", false),
	)
})
