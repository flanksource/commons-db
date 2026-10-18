package git_test

import (
	"context"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// recordingProvider is a pull-request provider that records requests.
type recordingProvider struct {
	opened []git.PullRequestRequest
	repos  []git.RepoURL
}

func (p *recordingProvider) Type() string                 { return models.ConnectionTypeGithub }
func (p *recordingProvider) Verify(context.Context) error { return nil }
func (p *recordingProvider) SupportsPullRequests() bool   { return true }
func (p *recordingProvider) ListRepositories(context.Context, git.ListRepositoriesOptions) ([]git.RepositoryInfo, error) {
	return nil, nil
}
func (p *recordingProvider) OpenPullRequest(_ context.Context, repo git.RepoURL, request git.PullRequestRequest) (git.PullRequest, error) {
	p.opened = append(p.opened, request)
	p.repos = append(p.repos, repo)
	return git.PullRequest{Number: 7, URL: "https://example.org/pr/7", State: git.PullRequestOpen,
		Head: request.Head, Base: request.Base, Title: request.Title}, nil
}
func (p *recordingProvider) GetPullRequest(context.Context, git.RepoURL, int) (git.PullRequest, error) {
	return git.PullRequest{}, nil
}
func (p *recordingProvider) ListPullRequests(context.Context, git.RepoURL, git.ListPullRequestsOptions) ([]git.PullRequest, error) {
	return nil, nil
}
func (p *recordingProvider) ClosePullRequest(context.Context, git.RepoURL, int) error { return nil }
func (p *recordingProvider) SetCommitStatus(context.Context, git.RepoURL, string, git.CommitStatus) error {
	return nil
}
func (p *recordingProvider) CommentPullRequest(context.Context, git.RepoURL, int, string) (string, error) {
	return "", nil
}

var _ = Describe("Cache.Apply", func() {
	var (
		fixture bareRepo
		cache   *git.Cache
	)
	BeforeEach(func() {
		fixture = newBareRepo("rules")
		cache = &git.Cache{Root: GinkgoT().TempDir()}
	})

	request := func() git.ChangeRequest {
		return git.ChangeRequest{Base: "main", Branch: "apply/run-1", Message: "apply run 1", Author: fixtureSignature,
			Files: []git.FileChange{{Path: "rules/br/new.xml", Content: []byte("<rule/>")}, {Path: "README.md", Delete: true}}}
	}

	It("pushes a branch and opens a pull request against the base", func(ctx SpecContext) {
		provider := &recordingProvider{}
		repo, err := git.NewRepository(git.RepositoryOptions{URL: fixture.FileURL(), Provider: provider, ProviderType: models.ConnectionTypeGit})
		Expect(err).ToNot(HaveOccurred())
		change := request()
		change.BaseSHA = fixture.Main
		change.PullRequest = &git.PullRequestRequest{Title: "Apply run 1", Body: "changes"}
		change.Tag = &git.TagOptions{Name: "apply-run-1", Message: "run 1"}

		result, err := cache.Apply(ctx, repo, change)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Branch).To(Equal("apply/run-1"))
		Expect(result.Tag).To(Equal("apply-run-1"))
		Expect(result.PullRequest).To(Equal(&git.PullRequest{Number: 7, URL: "https://example.org/pr/7",
			State: git.PullRequestOpen, Head: "apply/run-1", Base: "main", Title: "Apply run 1"}))
		Expect(provider.opened).To(Equal([]git.PullRequestRequest{{Title: "Apply run 1", Body: "changes", Head: "apply/run-1", Base: "main"}}))
		Expect(gitFixture(fixture.Path, "rev-parse", "refs/heads/apply/run-1")).To(Equal(result.CommitSHA))
		Expect(gitFixture(fixture.Path, "rev-parse", "apply/run-1^")).To(Equal(fixture.Main))
		Expect(gitFixture(fixture.Path, "ls-tree", "--name-only", "-r", "apply/run-1")).To(Equal("rules/br/new.xml"))
	})

	It("refuses a pull request on a provider without pull requests before pushing", func(ctx SpecContext) {
		change := request()
		change.PullRequest = &git.PullRequestRequest{Title: "x"}
		_, err := cache.Apply(ctx, newRepository(fixture.FileURL(), git.Auth{}), change)
		Expect(err).To(MatchError(git.ErrPullRequestsUnsupported))
		Expect(gitFixture(fixture.Path, "branch", "--list", "apply/run-1")).To(BeEmpty())
	})

	It("refuses a base whose head moved off the expected commit before pushing", func(ctx SpecContext) {
		change := request()
		change.BaseSHA = fixture.First
		_, err := cache.Apply(ctx, newRepository(fixture.FileURL(), git.Auth{}), change)
		Expect(err).To(MatchError(git.ErrBaseMoved))
		Expect(err).To(MatchError(ContainSubstring(fixture.Main)))
		Expect(gitFixture(fixture.Path, "branch", "--list", "apply/run-1")).To(BeEmpty())
	})

	It("refuses a base SHA that is not a full commit SHA", func(ctx SpecContext) {
		change := request()
		change.BaseSHA = fixture.Main[:12]
		_, err := cache.Apply(ctx, newRepository(fixture.FileURL(), git.Auth{}), change)
		Expect(err).To(MatchError(ContainSubstring("full 40-character")))
	})

	It("refuses an existing remote branch before pushing", func(ctx SpecContext) {
		change := request()
		change.Branch = "feature"
		_, err := cache.Apply(ctx, newRepository(fixture.FileURL(), git.Auth{}), change)
		Expect(err).To(MatchError(git.ErrBranchExists))
		Expect(gitFixture(fixture.Path, "rev-parse", "refs/heads/feature")).To(Equal(fixture.Feature))
	})

	It("requires a branch base for a pull request", func(ctx SpecContext) {
		provider := &recordingProvider{}
		repo, err := git.NewRepository(git.RepositoryOptions{URL: fixture.FileURL(), Provider: provider, ProviderType: models.ConnectionTypeGit})
		Expect(err).ToNot(HaveOccurred())
		change := request()
		change.Base = "v1"
		change.PullRequest = &git.PullRequestRequest{Title: "x"}
		_, err = cache.Apply(ctx, repo, change)
		Expect(err).To(MatchError(ContainSubstring("must be a branch")))
	})
})
