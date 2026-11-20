// A GitLab merge request's review: its required approvals as a policy, and each reviewer's state
// as an Azure-style vote (approved 10, changes requested -10, else 0).
package git

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
)

type gitlabUser struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// gitlabVotes are GitLab reviewer states as votes.
var gitlabVotes = map[string]int{"approved": 10, "requested_changes": -10}

func (p *gitlabProvider) PullRequestReview(ctx context.Context, repo RepoURL, number int) (PullRequestReview, error) {
	if number <= 0 {
		return PullRequestReview{}, fmt.Errorf("merge request number must be positive, got %d", number)
	}
	var approvals struct {
		Approved          bool `json:"approved"`
		ApprovalsRequired int  `json:"approvals_required"`
		ApprovedBy        []struct {
			User gitlabUser `json:"user"`
		} `json:"approved_by"`
	}
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.mrEndpoint(repo, fmt.Sprintf("/%d/approvals", number)), Out: &approvals}); err != nil {
		return PullRequestReview{}, fmt.Errorf("read gitlab merge request !%d approvals: %w", number, err)
	}
	var reviewers []struct {
		User  gitlabUser `json:"user"`
		State string     `json:"state"`
	}
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.mrEndpoint(repo, fmt.Sprintf("/%d/reviewers", number)), Out: &reviewers}); err != nil {
		return PullRequestReview{}, fmt.Errorf("read gitlab merge request !%d reviewers: %w", number, err)
	}
	review := PullRequestReview{Policies: []PullRequestPolicy{}, Reviewers: []PullRequestReviewer{}}
	if approvals.ApprovalsRequired > 0 {
		status := "rejected"
		if approvals.Approved {
			status = "approved"
		}
		review.Policies = append(review.Policies, PullRequestPolicy{Name: "Approvals", Status: status, Required: true,
			MinimumApproverCount: approvals.ApprovalsRequired})
	}
	seen := map[int64]bool{}
	for _, reviewer := range reviewers {
		seen[reviewer.User.ID] = true
		review.Reviewers = append(review.Reviewers, PullRequestReviewer{ID: strconv.FormatInt(reviewer.User.ID, 10), Name: reviewer.User.Name,
			Vote: gitlabVotes[reviewer.State]})
	}
	for _, approver := range approvals.ApprovedBy {
		if !seen[approver.User.ID] {
			review.Reviewers = append(review.Reviewers, PullRequestReviewer{ID: strconv.FormatInt(approver.User.ID, 10), Name: approver.User.Name, Vote: 10})
		}
	}
	return review, nil
}
