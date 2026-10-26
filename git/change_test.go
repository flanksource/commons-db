package git_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"

	rpchttp "github.com/flanksource/clicky/rpc/http"
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
func (p *recordingProvider) ListPullRequestActivity(context.Context, git.RepoURL, int) ([]git.PullRequestActivity, error) {
	return nil, nil
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

	Describe("without a worktree", func() {
		var base string
		BeforeEach(func() {
			work := filepath.Join(fixture.Root, "seed")
			gitFixture(fixture.Root, "clone", "-q", fixture.Path, work)
			writeFixtureFile(work, ".gitattributes", "*.xml text eol=lf\n")
			writeFixtureFile(work, "tools/run.sh", "#!/bin/sh\necho one\n")
			Expect(os.Chmod(filepath.Join(work, "tools/run.sh"), 0o755)).To(Succeed())
			gitFixture(work, "add", "-A")
			gitFixture(work, "commit", "-q", "-m", "tools")
			gitFixture(work, "push", "-q", "origin", "main")
			base = gitFixture(work, "rev-parse", "HEAD")
		})

		files := []git.FileChange{
			{Path: "rules/br/new.xml", Content: []byte("<rule>\r\n</rule>\r\n")},
			{Path: "tools/run.sh", Content: []byte("#!/bin/sh\necho two\n")},
			{Path: "README.md", Delete: true},
		}
		timestamp := regexp.MustCompile(`(?m) \d+ [+-]\d{4}$`)
		commit := func(sha string) string {
			return timestamp.ReplaceAllString(gitFixture(fixture.Path, "cat-file", "commit", sha), "")
		}

		It("commits the same tree and commit content as a worktree, with no worktree", func(ctx SpecContext) {
			change := func(branch string) git.ChangeRequest {
				return git.ChangeRequest{Base: "main", BaseSHA: base, Branch: branch, Author: fixtureSignature,
					Message: "apply run 1  \n\n\nbody line \n\n", Files: files}
			}
			repo := newRepository(fixture.FileURL(), git.Auth{})
			viaWorktree, err := git.ApplyInWorktree(ctx, cache, repo, change("apply/worktree"))
			Expect(err).ToNot(HaveOccurred())
			timed, timings := rpchttp.WithTimings(ctx)
			viaIndex, err := cache.Apply(timed, repo, change("apply/index"))
			Expect(err).ToNot(HaveOccurred())

			Expect(gitFixture(fixture.Path, "rev-parse", "apply/index^{tree}")).To(Equal(gitFixture(fixture.Path, "rev-parse", "apply/worktree^{tree}")))
			Expect(commit(viaIndex.CommitSHA)).To(Equal(commit(viaWorktree.CommitSHA)))
			Expect(gitFixture(fixture.Path, "ls-tree", "-r", "apply/index")).To(ContainSubstring("100755 blob"))
			Expect(gitFixture(fixture.Path, "cat-file", "-p", "apply/index:rules/br/new.xml")).To(Equal("<rule>\n</rule>"))
			worktreeOps, _ := timings.Counter("git_worktree", "ops")
			Expect(worktreeOps).To(BeZero())
		})

		It("refuses deleting a file the base lacks before pushing", func(ctx SpecContext) {
			change := git.ChangeRequest{Base: "main", Branch: "apply/missing", Message: "delete", Author: fixtureSignature,
				Files: []git.FileChange{{Path: "rules/absent.xml", Delete: true}}}
			_, err := cache.Apply(ctx, newRepository(fixture.FileURL(), git.Auth{}), change)
			Expect(err).To(MatchError(ContainSubstring("delete rules/absent.xml")))
			Expect(gitFixture(fixture.Path, "branch", "--list", "apply/missing")).To(BeEmpty())
		})
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
