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
