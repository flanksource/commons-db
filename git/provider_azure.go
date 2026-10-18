package git

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/flanksource/commons-db/models"
)

const azureAPIVersion = "7.1"

// azureProvider is the Azure DevOps REST API (Basic ":PAT" auth); its base
// URL is the organization (https://dev.azure.com/{org}).
type azureProvider struct {
	api     *restClient
	project string
}

type azureRepo struct {
	Name          string `json:"name"`
	RemoteURL     string `json:"remoteUrl"`
	SSHURL        string `json:"sshUrl"`
	WebURL        string `json:"webUrl"`
	DefaultBranch string `json:"defaultBranch"`
	IsDisabled    bool   `json:"isDisabled"`
	Project       struct {
		Name string `json:"name"`
	} `json:"project"`
}

type azurePull struct {
	ID            int    `json:"pullRequestId"`
	Status        string `json:"status"`
	Title         string `json:"title"`
	SourceRefName string `json:"sourceRefName"`
	TargetRefName string `json:"targetRefName"`
	CreatedBy     struct {
		ID string `json:"id"`
	} `json:"createdBy"`
	Repository struct {
		WebURL string `json:"webUrl"`
	} `json:"repository"`
}

func (p *azureProvider) Type() string               { return models.ConnectionTypeAzureDevops }
func (p *azureProvider) SupportsPullRequests() bool { return true }

func (p *azureProvider) Verify(ctx context.Context) error {
	_, err := p.api.do(ctx, http.MethodGet, p.endpoint("/_apis/projects"), nil, nil)
	return err
}

func (p *azureProvider) endpoint(path string) string {
	return p.api.endpoint(path, url.Values{"api-version": {azureAPIVersion}})
}

// ListRepositories lists a project's repositories, or the organization's
// when no project is set.
func (p *azureProvider) ListRepositories(ctx context.Context, options ListRepositoriesOptions) ([]RepositoryInfo, error) {
	path := "/_apis/git/repositories"
	if project := cmp.Or(options.Namespace, p.project); project != "" {
		path = "/" + url.PathEscape(project) + path
	}
	var page struct {
		Value []azureRepo `json:"value"`
	}
	if _, err := p.api.do(ctx, http.MethodGet, p.endpoint(path), nil, &page); err != nil {
		return nil, err
	}
	repos := make([]RepositoryInfo, 0, len(page.Value))
	for _, repo := range page.Value {
		repos = append(repos, RepositoryInfo{Name: repo.Name, FullName: repo.Project.Name + "/" + repo.Name,
			Namespace: repo.Project.Name, CloneURL: repo.RemoteURL, SSHURL: repo.SSHURL, WebURL: repo.WebURL,
			DefaultBranch: strings.TrimPrefix(repo.DefaultBranch, "refs/heads/"), Archived: repo.IsDisabled})
	}
	return repos, nil
}

// OpenPullRequest opens a pull request and, with AutoMerge, sets
// auto-complete as its creator. Reviewers and assignees are refused before
// anything is created: they need the identity API.
func (p *azureProvider) OpenPullRequest(ctx context.Context, repo RepoURL, request PullRequestRequest) (PullRequest, error) {
	if err := request.validate(); err != nil {
		return PullRequest{}, err
	}
	if len(request.Reviewers) > 0 || len(request.Assignees) > 0 {
		return PullRequest{}, unsupported("azure_devops", "reviewers and assignees (they need identity ids)")
	}
	body := map[string]any{"sourceRefName": "refs/heads/" + request.Head, "targetRefName": "refs/heads/" + request.Base,
		"title": request.Title, "description": request.Body, "isDraft": request.Draft}
	if len(request.Labels) > 0 {
		labels := make([]map[string]string, 0, len(request.Labels))
		for _, label := range request.Labels {
			labels = append(labels, map[string]string{"name": label})
		}
		body["labels"] = labels
	}
	var pull azurePull
	if _, err := p.api.do(ctx, http.MethodPost, p.pullEndpoint(repo, ""), body, &pull); err != nil {
		return PullRequest{}, err
	}
	if request.AutoMerge {
		autoComplete := map[string]any{"autoCompleteSetBy": map[string]string{"id": pull.CreatedBy.ID}}
		if _, err := p.api.do(ctx, http.MethodPatch, p.pullEndpoint(repo, fmt.Sprintf("/%d", pull.ID)), autoComplete, nil); err != nil {
			return PullRequest{}, fmt.Errorf("pull request %d opened, but enabling auto-complete failed: %w", pull.ID, err)
		}
	}
	return pull.toPullRequest()
}

func (p *azureProvider) GetPullRequest(ctx context.Context, repo RepoURL, number int) (PullRequest, error) {
	var pull azurePull
	if _, err := p.api.do(ctx, http.MethodGet, p.pullEndpoint(repo, fmt.Sprintf("/%d", number)), nil, &pull); err != nil {
		return PullRequest{}, err
	}
	return pull.toPullRequest()
}

// ClosePullRequest abandons the pull request.
func (p *azureProvider) ClosePullRequest(ctx context.Context, repo RepoURL, number int) error {
	_, err := p.api.do(ctx, http.MethodPatch, p.pullEndpoint(repo, fmt.Sprintf("/%d", number)), map[string]any{"status": "abandoned"}, nil)
	return err
}

func (p *azureProvider) pullEndpoint(repo RepoURL, suffix string) string {
	return p.endpoint("/" + url.PathEscape(repo.Project) + "/_apis/git/repositories/" + url.PathEscape(repo.Name) + "/pullrequests" + suffix)
}

func (pull azurePull) toPullRequest() (PullRequest, error) {
	states := map[string]PullRequestState{"active": PullRequestOpen, "abandoned": PullRequestClosed, "completed": PullRequestMerged}
	state, ok := states[pull.Status]
	if !ok {
		return PullRequest{}, fmt.Errorf("azure DevOps pull request %d has unknown status %q", pull.ID, pull.Status)
	}
	return PullRequest{Number: pull.ID, URL: fmt.Sprintf("%s/pullrequest/%d", pull.Repository.WebURL, pull.ID), State: state,
		Head: strings.TrimPrefix(pull.SourceRefName, "refs/heads/"), Base: strings.TrimPrefix(pull.TargetRefName, "refs/heads/"),
		Title: pull.Title}, nil
}
