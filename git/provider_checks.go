package git

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"
)

func azureMergeability(state string) string {
	switch state {
	case "succeeded":
		return "mergeable"
	case "conflicts":
		return "conflicts"
	case "queued", "notSet":
		return "checking"
	case "rejectedByPolicy":
		return "blocked"
	case "failure":
		return "error"
	default:
		return "unknown"
	}
}

func githubMergeability(mergeable *bool, state string) string {
	if state == "blocked" || state == "draft" {
		return "blocked"
	}
	if mergeable == nil {
		return "checking"
	}
	if !*mergeable {
		return "conflicts"
	}
	return "mergeable"
}

func gitlabMergeability(state string) string {
	switch state {
	case "mergeable":
		return "mergeable"
	case "conflict":
		return "conflicts"
	case "checking", "unchecked", "preparing", "approvals_syncing":
		return "checking"
	case "":
		return "unknown"
	default:
		return "blocked"
	}
}

func checkState(state string) (string, error) {
	switch state {
	case "success", "succeeded", "approved", "neutral", "skipped", "notApplicable":
		return "success", nil
	case "failure", "failed", "rejected", "timed_out", "cancelled", "action_required", "stale", "startup_failure":
		return "failure", nil
	case "error", "broken":
		return "error", nil
	case "notSet", "pending", "queued", "running", "in_progress", "created", "waiting", "requested", "scheduled", "manual", "expected":
		return "pending", nil
	default:
		return "", fmt.Errorf("unknown upstream check state %q", state)
	}
}

type azurePullStatus struct {
	ID        int       `json:"id"`
	UpdatedAt time.Time `json:"updatedDate"`
	State     string    `json:"state"`
	URL       string    `json:"targetUrl"`
	Context   struct {
		Genre string `json:"genre"`
		Name  string `json:"name"`
	} `json:"context"`
}

func (p *azureProvider) PullRequestChecks(ctx context.Context, repo RepoURL, pr PullRequest) ([]PullRequestCheck, error) {
	var result struct {
		Value []azurePullStatus `json:"value"`
	}
	if _, err := p.api.do(ctx, http.MethodGet, p.pullEndpoint(repo, fmt.Sprintf("/%d/statuses", pr.Number)), nil, &result); err != nil {
		return nil, err
	}
	slices.SortStableFunc(result.Value, func(a, b azurePullStatus) int {
		if order := b.UpdatedAt.Compare(a.UpdatedAt); order != 0 {
			return order
		}
		return cmp.Compare(b.ID, a.ID)
	})
	checks, seen := []PullRequestCheck{}, map[string]bool{}
	for _, status := range result.Value {
		name := status.Context.Name
		if status.Context.Genre != "" {
			name = status.Context.Genre + "/" + name
		}
		if seen[name] {
			continue
		}
		state, err := checkState(status.State)
		if err != nil {
			return nil, err
		}
		checks = append(checks, PullRequestCheck{Name: name, State: state, URL: status.URL})
		seen[name] = true
	}
	return p.azurePolicyChecks(ctx, repo, pr.Number, checks)
}

func (p *azureProvider) azurePolicyChecks(ctx context.Context, repo RepoURL, number int, checks []PullRequestCheck) ([]PullRequestCheck, error) {
	review, err := p.PullRequestReview(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	for _, policy := range review.Policies {
		state, err := checkState(policy.Status)
		if err != nil {
			return nil, err
		}
		checks = append(checks, PullRequestCheck{Name: policy.Name, State: state, URL: policy.URL, Required: policy.Required})
	}
	return checks, nil
}

func (p *githubProvider) PullRequestChecks(ctx context.Context, repo RepoURL, pr PullRequest) ([]PullRequestCheck, error) {
	checks, err := p.githubStatuses(ctx, repo, pr.HeadSHA)
	if err != nil {
		return nil, err
	}
	for page := 1; ; page++ {
		var result struct {
			CheckRuns []struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				URL        string `json:"html_url"`
			} `json:"check_runs"`
		}
		endpoint := p.repoEndpoint(repo, "/commits/"+url.PathEscape(pr.HeadSHA)+"/check-runs") + "?per_page=100&filter=latest&page=" + strconv.Itoa(page)
		if _, err := p.api.do(ctx, http.MethodGet, endpoint, nil, &result); err != nil {
			return nil, err
		}
		for _, check := range result.CheckRuns {
			state := check.Status
			if state == "completed" {
				state = check.Conclusion
			}
			normalized, err := checkState(state)
			if err != nil {
				return nil, err
			}
			checks = append(checks, PullRequestCheck{Name: check.Name, State: normalized, URL: check.URL})
		}
		if len(result.CheckRuns) < 100 {
			return checks, nil
		}
	}
}

func (p *githubProvider) githubStatuses(ctx context.Context, repo RepoURL, sha string) ([]PullRequestCheck, error) {
	checks := []PullRequestCheck{}
	for page := 1; ; page++ {
		var result struct {
			Statuses []struct {
				Name  string `json:"context"`
				State string `json:"state"`
				URL   string `json:"target_url"`
			} `json:"statuses"`
		}
		endpoint := p.repoEndpoint(repo, "/commits/"+url.PathEscape(sha)+"/status") + "?per_page=100&page=" + strconv.Itoa(page)
		if _, err := p.api.do(ctx, http.MethodGet, endpoint, nil, &result); err != nil {
			return nil, err
		}
		for _, status := range result.Statuses {
			state, err := checkState(status.State)
			if err != nil {
				return nil, err
			}
			checks = append(checks, PullRequestCheck{Name: status.Name, State: state, URL: status.URL})
		}
		if len(result.Statuses) < 100 {
			return checks, nil
		}
	}
}

func (p *gitlabProvider) PullRequestChecks(ctx context.Context, repo RepoURL, pr PullRequest) ([]PullRequestCheck, error) {
	checks := []PullRequestCheck{}
	for page := 1; ; page++ {
		var statuses []struct {
			Name  string `json:"name"`
			State string `json:"status"`
			URL   string `json:"target_url"`
		}
		endpoint := p.projectEndpoint(repo, "/repository/commits/"+url.PathEscape(pr.HeadSHA)+"/statuses") + "?per_page=100&page=" + strconv.Itoa(page)
		if _, err := p.api.do(ctx, http.MethodGet, endpoint, nil, &statuses); err != nil {
			return nil, err
		}
		for _, status := range statuses {
			state, err := checkState(status.State)
			if err != nil {
				return nil, err
			}
			checks = append(checks, PullRequestCheck{Name: status.Name, State: state, URL: status.URL})
		}
		if len(statuses) < 100 {
			return checks, nil
		}
	}
}
