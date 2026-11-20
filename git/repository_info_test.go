// Tests reading one repository's information from its provider: its identity and default branch,
// without refs/heads/.
package git_test

import (
	"net/http"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("An Azure DevOps repository's information", func() {
	It("names its default branch without refs/heads/", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /acme/Platform/_apis/git/repositories/rules": reply(`{"id":"rules-id","name":"rules","defaultBranch":"refs/heads/trunk",
				"remoteUrl":"https://dev.azure.com/acme/Platform/_git/rules","project":{"name":"Platform"}}`),
		})
		reader, ok := providerClient(models.ConnectionTypeAzureDevops, api.Server.URL+"/acme", nil).Provider.(git.RepositoryReader)
		Expect(ok).To(BeTrue())
		info, err := reader.Repository(ctx, repoURL("https://dev.azure.com/acme/Platform/_git/rules", ""))
		Expect(err).ToNot(HaveOccurred())
		Expect(info.ID).To(Equal("rules-id"))
		Expect(info.FullName).To(Equal("Platform/rules"))
		Expect(info.DefaultBranch).To(Equal("trunk"))
	})
})

var _ = Describe("A GitLab repository's information", func() {
	It("is its project, a nested group's by its whole path", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /api/v4/projects/acme%2Fplatform%2Fapi": reply(`{"id":42,"path":"api","path_with_namespace":"acme/platform/api",
				"default_branch":"trunk","visibility":"private","web_url":"https://gitlab.example.com/acme/platform/api",
				"http_url_to_repo":"https://gitlab.example.com/acme/platform/api.git","namespace":{"full_path":"acme/platform"}}`),
		})
		reader, ok := providerClient(models.ConnectionTypeGitlab, api.Server.URL, nil).Provider.(git.RepositoryReader)
		Expect(ok).To(BeTrue())
		info, err := reader.Repository(ctx, repoURL("https://gitlab.example.com/acme/platform/api.git", models.ConnectionTypeGitlab))
		Expect(err).ToNot(HaveOccurred())
		Expect(info).To(Equal(git.RepositoryInfo{ID: "42", Name: "api", FullName: "acme/platform/api", Namespace: "acme/platform",
			CloneURL: "https://gitlab.example.com/acme/platform/api.git", WebURL: "https://gitlab.example.com/acme/platform/api",
			DefaultBranch: "trunk", Private: true}))
	})
})
