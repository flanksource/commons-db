// Azure DevOps auto-complete: the connection's identity sets it on the reviewed head, so Azure
// completes the pull request with the strategy asked once the target's policies pass.
package git

import (
	"context"
	"fmt"
	"net/http"
)

// AutoMergePullRequest sets auto-complete on pull request number. Azure has no auto-complete for
// one commit, so it is set only while the head is still the one reviewed; a push after that is
// governed by the target's policies (such as resetting votes on a new push).
func (p *azureProvider) AutoMergePullRequest(ctx context.Context, repo RepoURL, number int, options MergePullRequestOptions) error {
	if err := options.validate(number); err != nil {
		return err
	}
	me, err := p.identity(ctx, "set auto-complete")
	if err != nil {
		return err
	}
	var pull azurePull
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.pullEndpoint(repo, fmt.Sprintf("/%d", number)), Out: &pull}); err != nil {
		return err
	}
	if head := pull.LastMergeSourceCommit.CommitID; head != options.HeadSHA {
		return fmt.Errorf("pull request %d's head is %s, not %s, the head that was reviewed", number, head, options.HeadSHA)
	}
	payload := map[string]any{
		"autoCompleteSetBy": map[string]string{"id": me},
		"completionOptions": azureCompletionOptions(options),
	}
	var updated struct {
		AutoCompleteSetBy struct {
			ID string `json:"id"`
		} `json:"autoCompleteSetBy"`
	}
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodPatch, Endpoint: p.pullEndpoint(repo, fmt.Sprintf("/%d", number)), Body: payload, Out: &updated}); err != nil {
		return err
	}
	if updated.AutoCompleteSetBy.ID == "" {
		return fmt.Errorf("azure DevOps didn't set auto-complete on pull request %d", number)
	}
	return nil
}
