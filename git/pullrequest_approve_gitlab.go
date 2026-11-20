// Approving a GitLab merge request as the connection's user, leaving a comment as a note.
package git

import (
	"context"
	"fmt"
	"net/http"
)

func (p *gitlabProvider) ApprovePullRequest(ctx context.Context, repo RepoURL, number int, comment string) error {
	if number <= 0 {
		return fmt.Errorf("merge request number must be positive")
	}
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodPost, Endpoint: p.mrEndpoint(repo, fmt.Sprintf("/%d/approve", number))}); err != nil {
		return err
	}
	if comment == "" {
		return nil
	}
	if _, err := p.CommentPullRequest(ctx, repo, number, comment); err != nil {
		return fmt.Errorf("gitlab merge request !%d was approved but its comment wasn't left: %w", number, err)
	}
	return nil
}
