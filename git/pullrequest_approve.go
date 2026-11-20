// Approving a pull request as the identity the connection authenticates as, with an optional
// comment left on it.
package git

import "context"

// PullRequestApprover approves a pull request as the connection's identity; a non-empty comment
// is left on the pull request too.
type PullRequestApprover interface {
	ApprovePullRequest(ctx context.Context, repo RepoURL, number int, comment string) error
}
