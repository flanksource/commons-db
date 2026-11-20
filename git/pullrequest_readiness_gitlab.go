// GitLab merge readiness: what the merge request's detailed merge status says stops it merging
// now, with its pipeline and GitLab's own merge check kept apart from what blocks it outright.
package git

import (
	"context"
	"fmt"
	"net/http"
)

// gitlabBlocks names the detailed merge statuses that block a merge request as the dashboard
// explains them; any other status but the ones PullRequestReadiness handles blocks it as GitLab
// names it.
var gitlabBlocks = map[string]string{
	"ci_must_pass":             "pipeline must succeed",
	"draft_status":             "MR is a draft",
	"discussions_not_resolved": "discussions are not resolved",
	"not_approved":             "approvals are missing",
}

func (p *gitlabProvider) PullRequestReadiness(ctx context.Context, repo RepoURL, number int) (MergeReadiness, error) {
	if number <= 0 {
		return MergeReadiness{}, fmt.Errorf("merge request number must be positive")
	}
	var mr gitlabMergeRequest
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.mrEndpoint(repo, fmt.Sprintf("/%d", number)), Out: &mr}); err != nil {
		return MergeReadiness{}, err
	}
	if mr.State != "opened" && mr.State != "locked" {
		return MergeReadiness{}, nil
	}
	status := mr.DetailedMergeStatus
	switch status {
	case "mergeable":
		return MergeReadiness{State: "ready"}, nil
	case "conflict":
		return MergeReadiness{State: "conflicting", Reasons: []string{"conflicts with target branch"}}, nil
	case "checking", "unchecked", "preparing", "approvals_syncing":
		return MergeReadiness{State: "pending", WaitingForMerge: true, Reasons: []string{"merge status: " + status}}, nil
	case "ci_still_running":
		return MergeReadiness{State: "pending", WaitingForBuilds: true, Reasons: []string{"pipeline is still running"}}, nil
	}
	if reason, ok := gitlabBlocks[status]; ok {
		return MergeReadiness{State: "blocked", Reasons: []string{reason}}, nil
	}
	return MergeReadiness{State: "blocked", Reasons: []string{"merge status: " + status}}, nil
}
