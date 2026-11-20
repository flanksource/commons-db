// Azure DevOps approval: the connection's identity, from the organization's connection data,
// votes approve (10) as a reviewer of the pull request, and a comment becomes a thread.
package git

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// azureApproveVote is the reviewer vote Azure DevOps records as "Approved".
const azureApproveVote = 10

func (p *azureProvider) ApprovePullRequest(ctx context.Context, repo RepoURL, number int, comment string) error {
	if number <= 0 {
		return fmt.Errorf("pull request number must be positive")
	}
	// connectionData names the identity the request authenticates as; it takes no api-version.
	var connection struct {
		AuthenticatedUser struct {
			ID string `json:"id"`
		} `json:"authenticatedUser"`
	}
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.api.endpoint("/_apis/connectionData", nil), Out: &connection}); err != nil {
		return fmt.Errorf("read the identity the connection authenticates as: %w", err)
	}
	me := connection.AuthenticatedUser.ID
	if me == "" {
		return fmt.Errorf("azure DevOps named no identity for the connection to approve as")
	}
	var reviewer struct {
		Vote int `json:"vote"`
	}
	endpoint := p.pullEndpoint(repo, fmt.Sprintf("/%d/reviewers/%s", number, url.PathEscape(me)))
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodPut, Endpoint: endpoint, Body: map[string]int{"vote": azureApproveVote}, Out: &reviewer}); err != nil {
		return err
	}
	if reviewer.Vote != azureApproveVote {
		return fmt.Errorf("azure DevOps recorded vote %d instead of approving pull request %d", reviewer.Vote, number)
	}
	if comment == "" {
		return nil
	}
	_, err := p.CommentPullRequest(ctx, repo, number, comment)
	return err
}
