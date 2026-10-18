package git_test

import (
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Repository.OpenPullRequest", func() {
	var (
		fixture  bareRepo
		provider *recordingProvider
		repo     *git.Repository
	)
	BeforeEach(func() {
		fixture = newBareRepo("rules")
		provider = &recordingProvider{}
		var err error
		repo, err = git.NewRepository(git.RepositoryOptions{URL: fixture.FileURL(), Provider: provider, ProviderType: models.ConnectionTypeGit})
		Expect(err).ToNot(HaveOccurred())
	})

	It("opens a pull request for an existing branch without pushing anything", func(ctx SpecContext) {
		request := git.PullRequestRequest{Title: "Promote feature", Head: "feature", Base: "main"}
		opened, err := repo.OpenPullRequest(ctx, request)
		Expect(err).ToNot(HaveOccurred())
		Expect(opened.Number).To(Equal(7))
		Expect(provider.opened).To(Equal([]git.PullRequestRequest{request}))
		Expect(provider.repos).To(Equal([]git.RepoURL{repo.URL}))
	})

	DescribeTable("refuses a head or base that is not a remote branch",
		func(ctx SpecContext, head, base, message string) {
			_, err := repo.OpenPullRequest(ctx, git.PullRequestRequest{Title: "x", Head: head, Base: base})
			Expect(err).To(MatchError(ContainSubstring(message)))
			Expect(provider.opened).To(BeEmpty())
		},
		Entry("missing head", "missing", "main", `head branch "missing"`),
		Entry("tag base", "feature", "v1", `base branch "v1"`),
		Entry("same branch", "main", "main", "same branch"),
	)

	It("returns ErrPullRequestsUnsupported for a plain git connection", func(ctx SpecContext) {
		client, err := git.FromConnection(dbcontext.New(), models.Connection{Type: models.ConnectionTypeGit, URL: fixture.FileURL()})
		Expect(err).ToNot(HaveOccurred())
		plain, err := client.Repository(fixture.FileURL())
		Expect(err).ToNot(HaveOccurred())
		_, err = plain.OpenPullRequest(ctx, git.PullRequestRequest{Title: "x", Head: "feature", Base: "main"})
		Expect(err).To(MatchError(git.ErrPullRequestsUnsupported))
	})
})
