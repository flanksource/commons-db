package git

import (
	"context"

	"github.com/flanksource/commons-db/models"
)

// genericProvider is a plain git remote: one repository, verified by listing
// its refs, with no pull requests.
type genericProvider struct {
	repo *Repository
}

func (p *genericProvider) Type() string               { return models.ConnectionTypeGit }
func (p *genericProvider) SupportsPullRequests() bool { return false }

func (p *genericProvider) Verify(ctx context.Context) error {
	_, err := p.repo.LsRemote(ctx)
	return err
}

func (p *genericProvider) ListRepositories(context.Context, ListRepositoriesOptions) ([]RepositoryInfo, error) {
	return []RepositoryInfo{{Name: p.repo.URL.Name, FullName: p.repo.URL.FullName(), Namespace: p.repo.URL.Owner,
		CloneURL: p.repo.URL.Redacted()}}, nil
}

func (p *genericProvider) OpenPullRequest(context.Context, RepoURL, PullRequestRequest) (PullRequest, error) {
	return PullRequest{}, ErrPullRequestsUnsupported
}

func (p *genericProvider) GetPullRequest(context.Context, RepoURL, int) (PullRequest, error) {
	return PullRequest{}, ErrPullRequestsUnsupported
}

func (p *genericProvider) ClosePullRequest(context.Context, RepoURL, int) error {
	return ErrPullRequestsUnsupported
}

func (p *genericProvider) SetCommitStatus(context.Context, RepoURL, string, CommitStatus) error {
	return unsupported("git", "commit statuses")
}

func (p *genericProvider) CommentPullRequest(context.Context, RepoURL, int, string) (string, error) {
	return "", unsupported("git", "pull request comments")
}
