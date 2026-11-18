package git_test

import (
	"net/http"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Git branch protection", func() {
	DescribeTable("reads provider protection for a branch containing a slash",
		func(ctx SpecContext, providerType, repository, endpoint string) {
			api := newFakeAPI(map[string]func(*http.Request) apiReply{"GET " + endpoint: reply(`{"protected":true}`)})
			provider := providerClient(providerType, api.Server.URL, nil).Provider
			repo := &git.Repository{URL: repoURL(repository, ""), Provider: provider}
			protected, err := repo.BranchProtected(ctx, "changes/work")
			Expect(err).NotTo(HaveOccurred())
			Expect(protected).To(BeTrue())
		},
		Entry("GitHub", models.ConnectionTypeGithub, "https://github.com/acme/rules", "/repos/acme/rules/branches/changes%2Fwork"),
		Entry("GitLab", models.ConnectionTypeGitlab, "https://gitlab.com/acme/rules", "/api/v4/projects/acme%2Frules/repository/branches/changes%2Fwork"),
	)

	It("reads Azure DevOps blocking policies for the full branch ref", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/Platform/_apis/git/repositories/rules":        reply(`{"id":"repo-id"}`),
			"GET /acme/Platform/_apis/git/repositories/repo-id/refs": reply(`{"value":[{"name":"refs/heads/changes/work"}]}`),
			"GET /acme/Platform/_apis/git/policy/configurations":     reply(`{"value":[{"isEnabled":true,"isBlocking":true}]}`),
		})
		provider := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider
		repo := &git.Repository{URL: repoURL("https://dev.azure.com/acme/Platform/_git/rules", ""), Provider: provider}
		protected, err := repo.BranchProtected(ctx, "changes/work")
		Expect(err).NotTo(HaveOccurred())
		Expect(protected).To(BeTrue())
		Expect(api.Calls()[2].Query).To(ContainSubstring("refName=refs%2Fheads%2Fchanges%2Fwork"))
	})
})
