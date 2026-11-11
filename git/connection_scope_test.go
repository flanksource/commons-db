package git_test

import (
	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func scopeOf(connectionType, url string, properties map[string]string) git.ConnectionScope {
	GinkgoHelper()
	scope, err := git.ScopeOf(models.Connection{Type: connectionType, URL: url, Properties: types.JSONStringMap(properties)})
	Expect(err).ToNot(HaveOccurred())
	return scope
}

var _ = Describe("ConnectionScope", func() {
	It("is read from the connection alone, without its credentials", func() {
		_, err := git.ScopeOf(models.Connection{Type: models.ConnectionTypeGithub})
		Expect(err).ToNot(HaveOccurred())
		_, err = git.ScopeOf(models.Connection{Type: models.ConnectionTypeGitlab})
		Expect(err).To(MatchError(ContainSubstring("API URL is required")))
		_, err = git.ScopeOf(models.Connection{Type: models.ConnectionTypeGitlab, URL: "http://gitlab.example.com"})
		Expect(err).To(MatchError(ContainSubstring("must be an HTTPS URL")))
		_, err = git.ScopeOf(models.Connection{Type: "http"})
		Expect(err).To(MatchError(ContainSubstring("not a git provider")))
	})

	DescribeTable("owns the repositories Client.Repository opens",
		func(scope git.ConnectionScope, repo, hint string, refusal string) {
			err := scope.Owns(repoURL(repo, hint))
			if refusal == "" {
				Expect(err).ToNot(HaveOccurred())
			} else {
				Expect(err).To(MatchError(ContainSubstring(refusal)))
			}
		},
		Entry("github.com through the public API", scopeOf(models.ConnectionTypeGithub, "", nil), "git@github.com:acme/widgets.git", "", ""),
		Entry("another host than the API serves", scopeOf(models.ConnectionTypeGithub, "", nil), "https://gitlab.com/acme/widgets", "", "host"),
		Entry("an owner outside the organization", scopeOf(models.ConnectionTypeGithub, "", map[string]string{"organization": "acme"}), "https://github.com/other/widgets", "", "namespace"),
		Entry("a GitLab subgroup of the group", scopeOf(models.ConnectionTypeGitlab, "https://gitlab.example.com", map[string]string{"group": "acme"}), "https://gitlab.example.com/acme/team/widgets", models.ConnectionTypeGitlab, ""),
		Entry("an Azure repository of the organization over ssh", scopeOf(models.ConnectionTypeAzureDevops, "https://dev.azure.com/acme", map[string]string{"project": "Platform"}), "git@ssh.dev.azure.com:v3/acme/Platform/rules", "", ""),
		Entry("an Azure repository of another project", scopeOf(models.ConnectionTypeAzureDevops, "https://dev.azure.com/acme", map[string]string{"project": "Platform"}), "https://dev.azure.com/acme/Other/_git/rules", "", "project"),
		Entry("any repository on a git connection's host", scopeOf(models.ConnectionTypeGit, "https://git.example.com/acme/rules.git", nil), "https://git.example.com/acme/other.git", "", ""),
		Entry("http on an https git connection", scopeOf(models.ConnectionTypeGit, "https://git.example.com/acme/rules.git", nil), "http://git.example.com/acme/rules.git", "", `scheme "http"`),
	)

	It("names a repository the way the provider lists it", func() {
		Expect([]string{
			scopeOf(models.ConnectionTypeGithub, "", nil).RepositoryName(repoURL("git@github.com:acme/widgets.git", "")),
			scopeOf(models.ConnectionTypeGitlab, "https://gitlab.example.com", nil).RepositoryName(repoURL("https://gitlab.example.com/acme/team/widgets", models.ConnectionTypeGitlab)),
			scopeOf(models.ConnectionTypeAzureDevops, "https://dev.azure.com/acme", nil).RepositoryName(repoURL("git@ssh.dev.azure.com:v3/acme/Platform/rules", "")),
			scopeOf(models.ConnectionTypeGit, "https://git.example.com/acme/rules.git", nil).RepositoryName(repoURL("https://git.example.com/acme/rules.git", "")),
		}).To(Equal([]string{"acme/widgets", "acme/team/widgets", "Platform/rules", ""}))
	})

	It("names the one repository of a git connection", func() {
		scope := scopeOf(models.ConnectionTypeGit, "https://git.example.com/acme/rules.git", nil)
		Expect([]bool{
			scope.Repository.SameRepository(repoURL("git@GIT.example.com:acme/Rules", "")),
			scope.Repository.SameRepository(repoURL("ssh://git@git.example.com:22/acme/rules.git", "")),
			scope.Repository.SameRepository(repoURL("https://git.example.com/acme/other.git", "")),
			scope.Repository.SameRepository(repoURL("https://git.other.example/acme/rules.git", "")),
		}).To(Equal([]bool{true, true, false, false}))
		Expect(scopeOf(models.ConnectionTypeGithub, "", nil).Repository).To(BeNil())
	})
})
