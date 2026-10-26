package git

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

func (p *azureProvider) PullRequestReview(ctx context.Context, repo RepoURL, number int) (PullRequestReview, error) {
	if number <= 0 {
		return PullRequestReview{}, fmt.Errorf("pull request number must be positive, got %d", number)
	}
	projectID, err := p.projectID(ctx, repo.Project)
	if err != nil {
		return PullRequestReview{}, err
	}
	review := PullRequestReview{Policies: []PullRequestPolicy{}, Reviewers: []PullRequestReviewer{}}
	for skip := 0; ; skip += 100 {
		query := url.Values{"api-version": {"7.1-preview.1"}, "artifactId": {fmt.Sprintf("vstfs:///CodeReview/CodeReviewId/%s/%d", projectID, number)}, "$top": {"100"}, "$skip": {strconv.Itoa(skip)}}
		var page struct {
			Value []azurePolicyEvaluation `json:"value"`
		}
		if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.api.endpoint("/"+url.PathEscape(repo.Project)+"/_apis/policy/evaluations", query), Out: &page}); err != nil {
			return PullRequestReview{}, fmt.Errorf("read Azure pull request #%d policies: %w", number, err)
		}
		for _, evaluation := range page.Value {
			if !evaluation.Configuration.IsEnabled || evaluation.Configuration.IsDeleted || evaluation.Status == "notApplicable" {
				continue
			}
			policy, err := evaluation.toPolicy(repo)
			if err != nil {
				return PullRequestReview{}, fmt.Errorf("Azure pull request #%d policy %d: %w", number, evaluation.Configuration.ID, err)
			}
			review.Policies = append(review.Policies, policy)
		}
		if len(page.Value) < 100 {
			break
		}
	}
	var reviewers struct {
		Value []struct {
			ID       string `json:"id"`
			Name     string `json:"displayName"`
			Required bool   `json:"isRequired"`
			Vote     int    `json:"vote"`
		} `json:"value"`
	}
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.pullEndpoint(repo, fmt.Sprintf("/%d/reviewers", number)), Out: &reviewers}); err != nil {
		return PullRequestReview{}, fmt.Errorf("read Azure pull request #%d reviewers: %w", number, err)
	}
	for _, reviewer := range reviewers.Value {
		if reviewer.ID == "" || reviewer.Name == "" {
			return PullRequestReview{}, fmt.Errorf("Azure pull request #%d has a reviewer without id or display name", number)
		}
		review.Reviewers = append(review.Reviewers, PullRequestReviewer{ID: reviewer.ID, Name: reviewer.Name, Required: reviewer.Required, Vote: reviewer.Vote})
	}
	return review, nil
}

// azureProjectIDs memoises each organization's project ids, which never change.
var azureProjectIDs sync.Map

func (p *azureProvider) projectID(ctx context.Context, project string) (string, error) {
	key := p.api.base.String() + "\x00" + strings.ToLower(project)
	if id, ok := azureProjectIDs.Load(key); ok {
		return id.(string), nil
	}
	var read struct {
		ID string `json:"id"`
	}
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.endpoint("/_apis/projects/" + url.PathEscape(project)), Out: &read}); err != nil {
		return "", fmt.Errorf("read Azure project %q: %w", project, err)
	}
	if read.ID == "" {
		return "", fmt.Errorf("Azure project %q has no id", project)
	}
	azureProjectIDs.Store(key, read.ID)
	return read.ID, nil
}

type azurePolicyEvaluation struct {
	Status        string `json:"status"`
	Configuration struct {
		ID         int  `json:"id"`
		IsBlocking bool `json:"isBlocking"`
		IsEnabled  bool `json:"isEnabled"`
		IsDeleted  bool `json:"isDeleted"`
		Type       struct {
			DisplayName string `json:"displayName"`
		} `json:"type"`
		Settings struct {
			DisplayName          string `json:"displayName"`
			MinimumApproverCount int    `json:"minimumApproverCount"`
		} `json:"settings"`
	} `json:"configuration"`
	Context struct {
		BuildID json.Number `json:"buildId"`
	} `json:"context"`
}

func (evaluation azurePolicyEvaluation) toPolicy(repo RepoURL) (PullRequestPolicy, error) {
	switch evaluation.Status {
	case "queued", "running", "approved", "rejected", "broken":
	default:
		return PullRequestPolicy{}, fmt.Errorf("unknown status %q", evaluation.Status)
	}
	name := evaluation.Configuration.Settings.DisplayName
	if name == "" {
		name = evaluation.Configuration.Type.DisplayName
	}
	if name == "" {
		return PullRequestPolicy{}, fmt.Errorf("missing display name")
	}
	policy := PullRequestPolicy{ID: evaluation.Configuration.ID, Name: name, Status: evaluation.Status, Required: evaluation.Configuration.IsBlocking, MinimumApproverCount: evaluation.Configuration.Settings.MinimumApproverCount}
	if evaluation.Context.BuildID != "" {
		buildID, err := strconv.Atoi(string(evaluation.Context.BuildID))
		if err != nil || buildID <= 0 {
			return PullRequestPolicy{}, fmt.Errorf("invalid build id %q", evaluation.Context.BuildID)
		}
		policy.BuildID = buildID
		base, _, ok := strings.Cut(repo.azureWebBase(), "/_git/")
		if !ok {
			return PullRequestPolicy{}, fmt.Errorf("Azure repository %s has no project web URL", repo.Redacted())
		}
		policy.URL = base + "/_build/results?" + url.Values{"buildId": {strconv.Itoa(buildID)}, "view": {"results"}}.Encode()
	}
	return policy, nil
}
