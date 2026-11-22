// Tests reading one Azure DevOps build by its ID: its definition, source version, state and the
// repository it built.
package git_test

import (
	"net/http"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("An Azure DevOps build read by its ID", func() {
	repo := func() git.RepoURL {
		return repoURL("https://dev.azure.com/acme/Project/_git/rules", models.ConnectionTypeAzureDevops)
	}
	reader := func(fake *fakeAPI) git.PipelineExecutionReader {
		provider := providerClient(models.ConnectionTypeAzureDevops, fake.Server.URL, map[string]string{"project": "Project"}).Provider
		reader, ok := provider.(git.PipelineExecutionReader)
		Expect(ok).To(BeTrue(), "Azure DevOps reads a build by its ID")
		return reader
	}

	It("is the build with the repository it built", func(ctx SpecContext) {
		fake := newFakeAPI(map[string]func(*http.Request) apiReply{
			"GET /Project/_apis/build/builds/91": reply(`{"id":91,"buildNumber":"20261020.1","status":"inProgress","sourceBranch":"refs/pull/7/merge",
				"sourceVersion":"2222222222222222222222222222222222222222","queueTime":"2026-10-20T10:00:00Z","definition":{"id":23},
				"repository":{"id":"repo-id","type":"TfsGit"},"requestedFor":{"displayName":"Alex"},"_links":{"web":{"href":"https://example.test/builds/91"}}}`),
		})
		build, err := reader(fake).PipelineExecution(ctx, repo(), "91")
		Expect(err).NotTo(HaveOccurred())
		Expect(build).To(Equal(git.PipelineExecution{ID: "91", Pipeline: "23", Number: "20261020.1", Name: "20261020.1", Branch: "refs/pull/7/merge",
			SHA: "2222222222222222222222222222222222222222", Status: "inProgress", Author: "Alex", RepositoryID: "repo-id",
			CreatedAt: build.CreatedAt, URL: "https://example.test/builds/91"}))
		Expect(build.CreatedAt.IsZero()).To(BeFalse())
	})

	It("refuses an ID that isn't a positive number before asking Azure DevOps", func(ctx SpecContext) {
		fake := newFakeAPI(nil)
		_, err := reader(fake).PipelineExecution(ctx, repo(), "../91")
		Expect(err).To(MatchError(`azure build ID "../91" is not a positive number`))
		Expect(fake.Calls()).To(BeEmpty())
	})
})
