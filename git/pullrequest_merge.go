// Completing a pull request: merging the head that was reviewed into its base with the strategy
// asked, through the base's policies.
package git

import (
	"context"
	"fmt"
)

// MergeMethod is how a pull request's commits land on its base.
type MergeMethod string

const (
	MergeCommit MergeMethod = "merge"
	MergeSquash MergeMethod = "squash"
	MergeRebase MergeMethod = "rebase"
)

// MergePullRequestOptions merges HeadSHA, the head that was reviewed: a head that moved since is
// not merged.
type MergePullRequestOptions struct {
	Method  MergeMethod
	HeadSHA string
}

func (o MergePullRequestOptions) validate(number int) error {
	if number <= 0 {
		return fmt.Errorf("pull request number must be positive")
	}
	if o.HeadSHA == "" {
		return fmt.Errorf("merging pull request %d needs the head SHA that was reviewed", number)
	}
	switch o.Method {
	case MergeCommit, MergeSquash, MergeRebase:
		return nil
	}
	return fmt.Errorf("merge method %q is not merge, squash or rebase", o.Method)
}

// PullRequestMerger completes a pull request, or has its provider queue the completion behind the
// base's policies; one the provider refuses is an error.
type PullRequestMerger interface {
	MergePullRequest(ctx context.Context, repo RepoURL, number int, options MergePullRequestOptions) error
}

// PullRequestAutoMerger has its provider complete a pull request on its own once the base's
// policies pass, merging with the strategy asked; it refuses a pull request whose head moved
// since HeadSHA was reviewed.
type PullRequestAutoMerger interface {
	AutoMergePullRequest(ctx context.Context, repo RepoURL, number int, options MergePullRequestOptions) error
}
