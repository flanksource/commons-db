// Azure DevOps merge readiness: the pull request's merge status and failure, its draft state,
// whether its merge computation caught up with its branches' tips, and its blocking policies.
package git

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// azureBuildPolicyType is the type ID of Azure DevOps's build validation policy.
const azureBuildPolicyType = "0609b952-1397-4640-95ec-e00a01b2c241"

func (p *azureProvider) PullRequestReadiness(ctx context.Context, repo RepoURL, number int) (MergeReadiness, error) {
	if number <= 0 {
		return MergeReadiness{}, fmt.Errorf("pull request number must be positive")
	}
	var pull struct {
		azurePull
		MergeFailure string `json:"mergeFailureMessage"`
	}
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.pullEndpoint(repo, fmt.Sprintf("/%d", number)), Out: &pull}); err != nil {
		return MergeReadiness{}, err
	}
	if pull.Status != "active" {
		return MergeReadiness{}, nil
	}
	readiness := MergeReadiness{State: "ready"}
	switch pull.MergeStatus {
	case "succeeded":
	case "conflicts":
		readiness.State = "conflicting"
		readiness.Reasons = append(readiness.Reasons, "conflicts with target branch")
	case "queued":
		readiness.State, readiness.WaitingForMerge = "pending", true
		readiness.Reasons = append(readiness.Reasons, "merge computation queued")
	case "failure", "rejectedByPolicy":
		readiness.State = "blocked"
		readiness.Reasons = append(readiness.Reasons, "merge "+pull.MergeStatus)
	default:
		readiness.State = "unknown"
		readiness.Reasons = append(readiness.Reasons, "merge status: "+pull.MergeStatus)
	}
	if pull.MergeFailure != "" {
		readiness.Reasons = append(readiness.Reasons, pull.MergeFailure)
	}
	if pull.IsDraft {
		if readiness.State == "ready" || readiness.State == "pending" {
			readiness.State = "blocked"
		}
		readiness.Reasons = append(readiness.Reasons, "PR is a draft")
	}
	stale, err := p.mergeStale(ctx, repo, pull.azurePull)
	if err != nil {
		return MergeReadiness{}, err
	}
	if stale {
		if readiness.State == "ready" {
			readiness.State = "pending"
		}
		readiness.WaitingForMerge = true
		readiness.Reasons = append(readiness.Reasons, "merge computation has not caught up to the source or target branch")
	}
	review, err := p.PullRequestReview(ctx, repo, number)
	if err != nil {
		return MergeReadiness{}, err
	}
	for _, policy := range review.Policies {
		if !policy.Required || policy.Status == "approved" {
			continue
		}
		switch {
		case (policy.Status == "queued" || policy.Status == "running") && policy.Build:
			readiness.WaitingForBuilds = true
			if readiness.State == "ready" {
				readiness.State = "pending"
			}
		case readiness.State == "ready" || readiness.State == "pending":
			readiness.State = "blocked"
		}
		readiness.Reasons = append(readiness.Reasons, fmt.Sprintf("%s (policy %d): %s", policy.Name, policy.ID, policy.Status))
	}
	return readiness, nil
}

// mergeStale reports whether the source or target branch moved past the commits the pull
// request's last merge computation used.
func (p *azureProvider) mergeStale(ctx context.Context, repo RepoURL, pull azurePull) (bool, error) {
	source := pull.Repository
	if pull.ForkSource != nil {
		source = pull.ForkSource.Repository
	}
	if source.ID == "" || source.Project.Name == "" || pull.Repository.ID == "" {
		return false, fmt.Errorf("azure DevOps pull request %d has no repository identity for its branches", pull.ID)
	}
	head, err := p.branchTip(ctx, source.Project.Name, source.ID, pull.SourceRefName)
	if err != nil {
		return false, err
	}
	if head != pull.LastMergeSourceCommit.CommitID {
		return true, nil
	}
	if pull.LastMergeTargetCommit.CommitID == "" {
		return false, nil
	}
	target, err := p.branchTip(ctx, repo.Project, pull.Repository.ID, pull.TargetRefName)
	if err != nil {
		return false, err
	}
	return target != pull.LastMergeTargetCommit.CommitID, nil
}

// branchTip is the commit branch (refs/heads/...) of repository points at.
func (p *azureProvider) branchTip(ctx context.Context, project, repository, branch string) (string, error) {
	var refs struct {
		Value []struct {
			Name     string `json:"name"`
			ObjectID string `json:"objectId"`
		} `json:"value"`
	}
	query := url.Values{"api-version": {azureAPIVersion}, "filter": {branch[len("refs/"):]}}
	endpoint := p.api.endpoint("/"+url.PathEscape(project)+"/_apis/git/repositories/"+url.PathEscape(repository)+"/refs", query)
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: endpoint, Out: &refs}); err != nil {
		return "", err
	}
	for _, ref := range refs.Value {
		if ref.Name == branch && ref.ObjectID != "" {
			return ref.ObjectID, nil
		}
	}
	return "", fmt.Errorf("azure DevOps branch %s was not found", branch)
}
