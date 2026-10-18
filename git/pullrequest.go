package git

import (
	"context"
	"fmt"
)

// OpenPullRequest opens a pull request from the existing remote branch Head
// into Base, pushing nothing: a whole-branch promotion. Both must be branches
// on the remote. A provider without pull requests returns
// ErrPullRequestsUnsupported before the remote is listed.
func (r *Repository) OpenPullRequest(ctx context.Context, request PullRequestRequest) (PullRequest, error) {
	if r.Provider == nil || !r.Provider.SupportsPullRequests() {
		return PullRequest{}, ErrPullRequestsUnsupported
	}
	if err := request.validate(); err != nil {
		return PullRequest{}, err
	}
	if request.Head == request.Base {
		return PullRequest{}, fmt.Errorf("pull request head and base are the same branch %q", request.Head)
	}
	refs, err := r.LsRemote(ctx)
	if err != nil {
		return PullRequest{}, err
	}
	for _, branch := range []struct{ role, name string }{{"head", request.Head}, {"base", request.Base}} {
		if _, ok := refs.findIn(refs.Branches, branch.name); !ok {
			return PullRequest{}, fmt.Errorf("pull request %s branch %q is not a branch on %s", branch.role, branch.name, r.URL.Redacted())
		}
	}
	opened, err := r.Provider.OpenPullRequest(ctx, r.URL, request)
	if err != nil {
		return PullRequest{}, fmt.Errorf("open pull request %s into %s on %s: %w", request.Head, request.Base, r.URL.Redacted(), err)
	}
	return opened, nil
}
