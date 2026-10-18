package git

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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
	LastMergeSourceCommit struct {
		CommitID string `json:"commitId"`
	} `json:"lastMergeSourceCommit"`
	ForkSource *struct {
		Repository azurePullRepo `json:"repository"`
	} `json:"forkSource"`
	Repository azurePullRepo `json:"repository"`
}

type azurePullRepo struct {
	Name    string `json:"name"`
	Project struct {
		Name string `json:"name"`
	} `json:"project"`
}

// fullName is "Project/repo", as ListRepositories names a repository.
func (r azurePullRepo) fullName() string { return r.Project.Name + "/" + r.Name }

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
	return pull.toPullRequest(repo)
}

func (p *azureProvider) GetPullRequest(ctx context.Context, repo RepoURL, number int) (PullRequest, error) {
	var pull azurePull
	if _, err := p.api.do(ctx, http.MethodGet, p.pullEndpoint(repo, fmt.Sprintf("/%d", number)), nil, &pull); err != nil {
		return PullRequest{}, err
	}
	return pull.toPullRequest(repo)
}

var azureListStatuses = map[PullRequestState]string{PullRequestOpen: "active", PullRequestClosed: "abandoned", PullRequestMerged: "completed"}

func (p *azureProvider) ListPullRequests(ctx context.Context, repo RepoURL, options ListPullRequestsOptions) ([]PullRequest, error) {
	options, err := options.normalize()
	if err != nil {
		return nil, err
	}
	query := url.Values{"searchCriteria.status": {azureListStatuses[options.State]}}
	if options.Base != "" {
		query.Set("searchCriteria.targetRefName", "refs/heads/"+options.Base)
	}
	if options.Head != "" {
		query.Set("searchCriteria.sourceRefName", "refs/heads/"+options.Head)
	}
	return listPullRequestPages(options, func(page, perPage int) ([]PullRequest, int, error) {
		query.Set("$top", strconv.Itoa(perPage))
		query.Set("$skip", strconv.Itoa((page-1)*perPage))
		var list struct {
			Value []azurePull `json:"value"`
		}
		if _, err := p.api.do(ctx, http.MethodGet, p.pullEndpoint(repo, "")+"&"+query.Encode(), nil, &list); err != nil {
			return nil, 0, err
		}
		pulls := make([]PullRequest, 0, len(list.Value))
		for _, pull := range list.Value {
			converted, err := pull.toPullRequest(repo)
			if err != nil {
				return nil, 0, err
			}
			pulls = append(pulls, converted)
		}
		return pulls, len(list.Value), nil
	})
}

// ClosePullRequest abandons the pull request.
func (p *azureProvider) ClosePullRequest(ctx context.Context, repo RepoURL, number int) error {
	_, err := p.api.do(ctx, http.MethodPatch, p.pullEndpoint(repo, fmt.Sprintf("/%d", number)), map[string]any{"status": "abandoned"}, nil)
	return err
}

func (p *azureProvider) pullEndpoint(repo RepoURL, suffix string) string {
	return p.repoEndpoint(repo, "/pullrequests"+suffix)
}

func (p *azureProvider) repoEndpoint(repo RepoURL, suffix string) string {
	return p.endpoint("/" + url.PathEscape(repo.Project) + "/_apis/git/repositories/" + url.PathEscape(repo.Name) + suffix)
}

func (pull azurePull) toPullRequest(repo RepoURL) (PullRequest, error) {
	states := map[string]PullRequestState{"active": PullRequestOpen, "abandoned": PullRequestClosed, "completed": PullRequestMerged}
	state, ok := states[pull.Status]
	if !ok {
		return PullRequest{}, fmt.Errorf("azure DevOps pull request %d has unknown status %q", pull.ID, pull.Status)
	}
	head := pull.Repository
	if pull.ForkSource != nil {
		head = pull.ForkSource.Repository
	}
	if head.Name == "" || head.Project.Name == "" {
		return PullRequest{}, fmt.Errorf("azure DevOps pull request %d has no head repository name and project", pull.ID)
	}
	return PullRequest{Number: pull.ID, URL: fmt.Sprintf("%s/pullrequest/%d", repo.azureWebBase(), pull.ID), State: state,
		Head: strings.TrimPrefix(pull.SourceRefName, "refs/heads/"), Base: strings.TrimPrefix(pull.TargetRefName, "refs/heads/"),
		Title: pull.Title, HeadSHA: pull.LastMergeSourceCommit.CommitID, HeadRepository: head.fullName()}, nil
}

var azureCommitStates = map[CommitState]string{CommitStatePending: "pending", CommitStateSuccess: "succeeded",
	CommitStateFailure: "failed", CommitStateError: "error"}

// SetCommitStatus posts a commit status. Azure DevOps names a status by
// genre and name; Context splits at its last "/" ("oipa-cli/apply" is genre
// "oipa-cli", name "apply"), which Azure DevOps displays back as Context.
func (p *azureProvider) SetCommitStatus(ctx context.Context, repo RepoURL, sha string, status CommitStatus) error {
	state, err := validateCommitStatus(sha, status, azureCommitStates)
	if err != nil {
		return err
	}
	statusContext := map[string]string{"name": status.Context}
	if slash := strings.LastIndex(status.Context, "/"); slash > 0 && slash < len(status.Context)-1 {
		statusContext = map[string]string{"genre": status.Context[:slash], "name": status.Context[slash+1:]}
	}
	body := status.optionalFields(map[string]any{"state": state, "context": statusContext}, "description", "targetUrl")
	_, err = p.api.do(ctx, http.MethodPost, p.repoEndpoint(repo, "/commits/"+url.PathEscape(sha)+"/statuses"), body, nil)
	return err
}

// CommentPullRequest starts an active comment thread and returns the pull
// request's web URL (as GetPullRequest builds it) with ?discussionId=<thread
// id>, which opens that thread
// (the threads API returns only REST links).
func (p *azureProvider) CommentPullRequest(ctx context.Context, repo RepoURL, number int, body string) (string, error) {
	if err := validateComment(number, body); err != nil {
		return "", err
	}
	pull, err := p.GetPullRequest(ctx, repo, number)
	if err != nil {
		return "", err
	}
	thread := map[string]any{"status": "active",
		"comments": []map[string]any{{"parentCommentId": 0, "content": body, "commentType": "text"}}}
	var created struct {
		ID int `json:"id"`
	}
	if _, err := p.api.do(ctx, http.MethodPost, p.pullEndpoint(repo, fmt.Sprintf("/%d/threads", number)), thread, &created); err != nil {
		return "", err
	}
	if created.ID == 0 {
		return "", fmt.Errorf("azure DevOps pull request %d comment thread created without an id", number)
	}
	return fmt.Sprintf("%s?discussionId=%d", pull.URL, created.ID), nil
}
