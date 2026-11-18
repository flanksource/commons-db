package git

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

type branchProtectionProvider interface {
	branchProtected(context.Context, RepoURL, string) (bool, error)
}

// BranchProtected reports protection for an existing branch. With plain Git,
// only the remote can enforce its branch rules.
func (r *Repository) BranchProtected(ctx context.Context, branch string) (bool, error) {
	if err := ValidateBranchName(branch); err != nil {
		return false, err
	}
	if p, ok := r.Provider.(branchProtectionProvider); ok {
		return p.branchProtected(ctx, r.URL, branch)
	}
	if r.Provider == nil || !r.Provider.SupportsPullRequests() {
		return false, nil
	}
	return false, fmt.Errorf("%s cannot verify protection of branch %q", r.Provider.Type(), branch)
}

func (p *githubProvider) branchProtected(ctx context.Context, repo RepoURL, branch string) (bool, error) {
	return readBranchProtection(ctx, p.api, p.repoEndpoint(repo, "/branches/"+url.PathEscape(branch)))
}

func (p *gitlabProvider) branchProtected(ctx context.Context, repo RepoURL, branch string) (bool, error) {
	return readBranchProtection(ctx, p.api, p.projectEndpoint(repo, "/repository/branches/"+url.PathEscape(branch)))
}

func readBranchProtection(ctx context.Context, api *restClient, endpoint string) (bool, error) {
	var result struct {
		Protected *bool `json:"protected"`
	}
	if _, err := api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: endpoint, Out: &result}); err != nil {
		return false, err
	}
	if result.Protected == nil {
		return false, fmt.Errorf("provider did not report branch protection")
	}
	return *result.Protected, nil
}

func (p *azureProvider) branchProtected(ctx context.Context, repo RepoURL, branch string) (bool, error) {
	var repository struct {
		ID string `json:"id"`
	}
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.repoEndpoint(repo, ""), Out: &repository}); err != nil {
		return false, err
	}
	if repository.ID == "" {
		return false, fmt.Errorf("repository ID missing from Azure DevOps response")
	}
	full := "refs/heads/" + branch
	query := url.Values{"filter": {"heads/" + branch}, "api-version": {azureAPIVersion}}
	var refs struct {
		Value []struct {
			Name     string    `json:"name"`
			Locked   bool      `json:"isLocked"`
			LockedBy *struct{} `json:"isLockedBy"`
		}
	}
	endpoint := p.api.endpoint("/"+url.PathEscape(repo.Project)+"/_apis/git/repositories/"+url.PathEscape(repository.ID)+"/refs", query)
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: endpoint, Out: &refs}); err != nil {
		return false, err
	}
	for _, ref := range refs.Value {
		if ref.Name == full {
			if ref.Locked || ref.LockedBy != nil {
				return true, nil
			}
			return p.branchPolicyBlocksPush(ctx, repo.Project, repository.ID, full)
		}
	}
	return false, fmt.Errorf("branch %q was not found in Azure DevOps", branch)
}

func (p *azureProvider) branchPolicyBlocksPush(ctx context.Context, project, id, ref string) (bool, error) {
	query := url.Values{"repositoryId": {id}, "refName": {ref}, "api-version": {azureAPIVersion}}
	for {
		var policies struct {
			Value []struct {
				Enabled  bool `json:"isEnabled"`
				Blocking bool `json:"isBlocking"`
			}
		}
		endpoint := p.api.endpoint("/"+url.PathEscape(project)+"/_apis/git/policy/configurations", query)
		headers, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: endpoint, Out: &policies})
		if err != nil {
			return false, err
		}
		for _, policy := range policies.Value {
			if policy.Enabled && policy.Blocking {
				return true, nil
			}
		}
		next := headers.Get("x-ms-continuationtoken")
		if next == "" {
			return false, nil
		}
		if next == query.Get("continuationToken") {
			return false, fmt.Errorf("repeated branch policy continuation token from Azure DevOps")
		}
		query.Set("continuationToken", next)
	}
}
