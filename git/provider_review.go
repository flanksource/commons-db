package git

import "context"

// PullRequestReviewReader reads a host's current review and merge requirements.
type PullRequestReviewReader interface {
	PullRequestReview(ctx context.Context, repo RepoURL, number int) (PullRequestReview, error)
}

type PullRequestReview struct {
	Policies  []PullRequestPolicy
	Reviewers []PullRequestReviewer
}

type PullRequestPolicy struct {
	ID                   int
	Name                 string
	Status               string
	Required             bool
	BuildID              int
	URL                  string
	MinimumApproverCount int
}

type PullRequestReviewer struct {
	ID       string
	Name     string
	Required bool
	Vote     int
}
