// A pull request's review as hosts report it: the policies evaluated on it and its reviewers'
// votes, read through PullRequestReviewReader.
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
	ID       int
	Name     string
	Status   string
	Required bool
	BuildID  int
	// Build is a build validation policy, which waits on a pipeline rather than on people.
	Build                bool
	URL                  string
	MinimumApproverCount int
	// RequiredReviewerIDs are the reviewers a required reviewers policy asks for, by reviewer ID.
	RequiredReviewerIDs []string
	// FilenamePatterns are the paths a policy applies to; none applies it to every change.
	FilenamePatterns []string
}

type PullRequestReviewer struct {
	ID       string
	Name     string
	Required bool
	Vote     int
	// Login and AvatarURL are the reviewer's sign-in name and picture, where the provider names them.
	Login     string
	AvatarURL string
	// Group is a team or group reviewer, which has no login of its own.
	Group bool
}
