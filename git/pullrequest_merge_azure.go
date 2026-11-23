// Azure DevOps pull request completion: the reviewed source commit, completed with the strategy
// asked through the target's policies, keeping the source branch.
package git

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

var azureMergeStrategies = map[MergeMethod]string{MergeWithCommit: "noFastForward", MergeSquash: "squash", MergeRebase: "rebase"}

func (p *azureProvider) MergePullRequest(ctx context.Context, repo RepoURL, number int, options MergePullRequestOptions) error {
	if err := options.validate(number); err != nil {
		return err
	}
	payload := map[string]any{
		"status":                "completed",
		"lastMergeSourceCommit": map[string]string{"commitId": options.HeadSHA},
		"completionOptions":     map[string]any{"mergeStrategy": azureMergeStrategies[options.Method], "bypassPolicy": false, "deleteSourceBranch": false},
	}
	var completed struct {
		ID           int       `json:"pullRequestId"`
		Status       string    `json:"status"`
		MergeStatus  string    `json:"mergeStatus"`
		MergeFailure string    `json:"mergeFailureMessage"`
		QueuedAt     time.Time `json:"completionQueueTime"`
	}
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodPatch, Endpoint: p.pullEndpoint(repo, fmt.Sprintf("/%d", number)), Body: payload, Out: &completed}); err != nil {
		return err
	}
	queued := completed.Status == "active" && !completed.QueuedAt.IsZero() && completed.MergeFailure == ""
	if completed.ID == number && (completed.Status == "completed" || queued) {
		return nil
	}
	err := fmt.Errorf("azure DevOps did not complete pull request %d: status %q, merge status %q", number, completed.Status, completed.MergeStatus)
	if completed.MergeFailure != "" {
		err = fmt.Errorf("%w: %s", err, completed.MergeFailure)
	}
	return err
}
