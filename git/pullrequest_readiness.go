// Whether a pull request can merge now: its state (ready, pending, blocked, conflicting or unknown)
// and why, from its merge computation, draft state and blocking policies.
package git

import "context"

// MergeReadiness is whether an open pull request can merge now and why not: State is ready,
// pending (waiting on builds or on its merge computation), blocked, conflicting or unknown. A pull
// request that isn't open has no readiness, an empty State.
type MergeReadiness struct {
	State            string   `json:"state,omitempty"`
	Reasons          []string `json:"reasons,omitempty"`
	WaitingForBuilds bool     `json:"waitingForBuilds,omitempty"`
	WaitingForMerge  bool     `json:"waitingForMerge,omitempty"`
}

// PullRequestReadinessReader reads a pull request's merge readiness.
type PullRequestReadinessReader interface {
	PullRequestReadiness(ctx context.Context, repo RepoURL, number int) (MergeReadiness, error)
}

// MergeSnapshot is whether a pull request's source branch, and else its target branch, moved past
// the commits its provider's last trial merge used.
type MergeSnapshot struct {
	SourceMoved bool
	TargetMoved bool
}

// PullRequestMergeSnapshotReader reads whether a pull request's last trial merge is still of its
// branches' tips, without reading its policies.
type PullRequestMergeSnapshotReader interface {
	PullRequestMergeSnapshot(ctx context.Context, repo RepoURL, number int) (MergeSnapshot, error)
}
