// Merging a GitLab merge request: the head that was reviewed, as a merge commit or squashed, with
// the message asked, keeping its source branch. GitLab rebases in a request of its own, so a rebase is refused.
package git

import (
	"context"
	"fmt"
	"net/http"
)

func (p *gitlabProvider) MergePullRequest(ctx context.Context, repo RepoURL, number int, options MergePullRequestOptions) error {
	if err := options.validate(number); err != nil {
		return err
	}
	if options.Method == MergeRebase {
		return fmt.Errorf("gitlab merges merge request !%d with merge or squash; rebase it on GitLab, then merge it", number)
	}
	var merged gitlabMergeRequest
	body := map[string]any{"sha": options.HeadSHA, "squash": options.Method == MergeSquash, "should_remove_source_branch": false}
	switch {
	case options.CommitMessage == "":
	case options.Method == MergeSquash:
		body["squash_commit_message"] = options.CommitMessage
	default:
		body["merge_commit_message"] = options.CommitMessage
	}
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodPut, Endpoint: p.mrEndpoint(repo, fmt.Sprintf("/%d/merge", number)), Body: body, Out: &merged}); err != nil {
		return err
	}
	if merged.State != "merged" {
		return fmt.Errorf("gitlab left merge request !%d %s instead of merging it", number, merged.State)
	}
	return nil
}
