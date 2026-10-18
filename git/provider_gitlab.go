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

// gitlabProvider is the GitLab REST API v4 (gitlab.com or self-managed).
type gitlabProvider struct {
	api       *restClient
	namespace string
}

type gitlabProject struct {
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
	SSHURLToRepo      string `json:"ssh_url_to_repo"`
	WebURL            string `json:"web_url"`
	DefaultBranch     string `json:"default_branch"`
	Visibility        string `json:"visibility"`
	Archived          bool   `json:"archived"`
	Namespace         struct {
		FullPath string `json:"full_path"`
	} `json:"namespace"`
}

type gitlabMergeRequest struct {
	IID          int    `json:"iid"`
	WebURL       string `json:"web_url"`
	State        string `json:"state"`
	Title        string `json:"title"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	SHA          string `json:"sha"`
	SourceID     int    `json:"source_project_id"`
	TargetID     int    `json:"target_project_id"`
}

func (p *gitlabProvider) Type() string               { return models.ConnectionTypeGitlab }
func (p *gitlabProvider) SupportsPullRequests() bool { return true }

func (p *gitlabProvider) Verify(ctx context.Context) error {
	_, err := p.api.do(ctx, http.MethodGet, p.api.endpoint("/api/v4/user", nil), nil, nil)
	return err
}

// ListRepositories lists a group's projects (with subgroups), or every project
// the caller is a member of, paging by X-Next-Page.
func (p *gitlabProvider) ListRepositories(ctx context.Context, options ListRepositoriesOptions) ([]RepositoryInfo, error) {
	path, query := "/api/v4/projects", url.Values{"membership": {"true"}, "per_page": {"100"}}
	if namespace := cmp.Or(options.Namespace, p.namespace); namespace != "" {
		path, query = "/api/v4/groups/"+url.PathEscape(namespace)+"/projects", url.Values{"include_subgroups": {"true"}, "per_page": {"100"}}
	}
	var repos []RepositoryInfo
	for {
		var page []gitlabProject
		headers, err := p.api.do(ctx, http.MethodGet, p.api.endpoint(path, query), nil, &page)
		if err != nil {
			return nil, err
		}
		for _, project := range page {
			repos = append(repos, RepositoryInfo{Name: project.Path, FullName: project.PathWithNamespace,
				Namespace: project.Namespace.FullPath, CloneURL: project.HTTPURLToRepo, SSHURL: project.SSHURLToRepo,
				WebURL: project.WebURL, DefaultBranch: project.DefaultBranch, Private: project.Visibility == "private",
				Archived: project.Archived})
		}
		next := strings.TrimSpace(headers.Get("X-Next-Page"))
		if next == "" {
			return repos, nil
		}
		query.Set("page", next)
	}
}

// OpenPullRequest opens a merge request, resolving assignee and reviewer
// usernames to ids first, and sets auto-merge when requested.
func (p *gitlabProvider) OpenPullRequest(ctx context.Context, repo RepoURL, request PullRequestRequest) (PullRequest, error) {
	if err := request.validate(); err != nil {
		return PullRequest{}, err
	}
	assignees, err := p.userIDs(ctx, request.Assignees)
	if err != nil {
		return PullRequest{}, err
	}
	reviewers, err := p.userIDs(ctx, request.Reviewers)
	if err != nil {
		return PullRequest{}, err
	}
	title := request.Title
	if request.Draft {
		title = "Draft: " + title
	}
	body := map[string]any{"source_branch": request.Head, "target_branch": request.Base, "title": title, "description": request.Body}
	if len(request.Labels) > 0 {
		body["labels"] = strings.Join(request.Labels, ",")
	}
	if len(assignees) > 0 {
		body["assignee_ids"] = assignees
	}
	if len(reviewers) > 0 {
		body["reviewer_ids"] = reviewers
	}
	var mr gitlabMergeRequest
	if _, err := p.api.do(ctx, http.MethodPost, p.mrEndpoint(repo, ""), body, &mr); err != nil {
		return PullRequest{}, err
	}
	if request.AutoMerge {
		autoMerge := map[string]any{"merge_when_pipeline_succeeds": true}
		if _, err := p.api.do(ctx, http.MethodPut, p.mrEndpoint(repo, fmt.Sprintf("/%d/merge", mr.IID)), autoMerge, nil); err != nil {
			return PullRequest{}, fmt.Errorf("merge request %s opened, but enabling auto-merge failed: %w", mr.WebURL, err)
		}
	}
	return p.pullRequest(ctx, repo, mr)
}

func (p *gitlabProvider) userIDs(ctx context.Context, usernames []string) ([]int, error) {
	ids := make([]int, 0, len(usernames))
	for _, username := range usernames {
		var users []struct {
			ID int `json:"id"`
		}
		if _, err := p.api.do(ctx, http.MethodGet, p.api.endpoint("/api/v4/users", url.Values{"username": {username}}), nil, &users); err != nil {
			return nil, err
		}
		if len(users) != 1 {
			return nil, fmt.Errorf("gitlab user %q: expected exactly one match, found %d", username, len(users))
		}
		ids = append(ids, users[0].ID)
	}
	return ids, nil
}

func (p *gitlabProvider) GetPullRequest(ctx context.Context, repo RepoURL, number int) (PullRequest, error) {
	var mr gitlabMergeRequest
	if _, err := p.api.do(ctx, http.MethodGet, p.mrEndpoint(repo, fmt.Sprintf("/%d", number)), nil, &mr); err != nil {
		return PullRequest{}, err
	}
	return p.pullRequest(ctx, repo, mr)
}

var gitlabListStates = map[PullRequestState]string{PullRequestOpen: "opened", PullRequestClosed: "closed", PullRequestMerged: "merged"}

func (p *gitlabProvider) ListPullRequests(ctx context.Context, repo RepoURL, options ListPullRequestsOptions) ([]PullRequest, error) {
	options, err := options.normalize()
	if err != nil {
		return nil, err
	}
	query := url.Values{"state": {gitlabListStates[options.State]}}
	if options.Base != "" {
		query.Set("target_branch", options.Base)
	}
	if options.Head != "" {
		query.Set("source_branch", options.Head)
	}
	return listPullRequestPages(options, func(page, perPage int) ([]PullRequest, int, error) {
		query.Set("page", strconv.Itoa(page))
		query.Set("per_page", strconv.Itoa(perPage))
		var mrs []gitlabMergeRequest
		if _, err := p.api.do(ctx, http.MethodGet, p.mrEndpoint(repo, "")+"?"+query.Encode(), nil, &mrs); err != nil {
			return nil, 0, err
		}
		pulls := make([]PullRequest, 0, len(mrs))
		for _, mr := range mrs {
			pull, err := p.pullRequest(ctx, repo, mr)
			if err != nil {
				return nil, 0, err
			}
			pulls = append(pulls, pull)
		}
		return pulls, len(mrs), nil
	})
}

func (p *gitlabProvider) ClosePullRequest(ctx context.Context, repo RepoURL, number int) error {
	_, err := p.api.do(ctx, http.MethodPut, p.mrEndpoint(repo, fmt.Sprintf("/%d", number)), map[string]any{"state_event": "close"}, nil)
	return err
}

func (p *gitlabProvider) mrEndpoint(repo RepoURL, suffix string) string {
	return p.projectEndpoint(repo, "/merge_requests"+suffix)
}

func (p *gitlabProvider) projectEndpoint(repo RepoURL, suffix string) string {
	return p.api.endpoint("/api/v4/projects/"+url.PathEscape(repo.Owner+"/"+repo.Name)+suffix, nil)
}

func (mr gitlabMergeRequest) toPullRequest() (PullRequest, error) {
	states := map[string]PullRequestState{"opened": PullRequestOpen, "locked": PullRequestOpen,
		"closed": PullRequestClosed, "merged": PullRequestMerged}
	state, ok := states[mr.State]
	if !ok {
		return PullRequest{}, fmt.Errorf("gitlab merge request !%d has unknown state %q", mr.IID, mr.State)
	}
	return PullRequest{Number: mr.IID, URL: mr.WebURL, State: state, Head: mr.SourceBranch, Base: mr.TargetBranch,
		Title: mr.Title, HeadSHA: mr.SHA}, nil
}

// pullRequest converts mr, naming its head repository by path_with_namespace:
// repo's own path when the source project is the target project, else the
// source (fork) project looked up by id.
func (p *gitlabProvider) pullRequest(ctx context.Context, repo RepoURL, mr gitlabMergeRequest) (PullRequest, error) {
	pr, err := mr.toPullRequest()
	if err != nil {
		return PullRequest{}, err
	}
	if mr.SourceID == mr.TargetID {
		pr.HeadRepository = repo.Owner + "/" + repo.Name
		return pr, nil
	}
	var source gitlabProject
	if _, err := p.api.do(ctx, http.MethodGet, p.api.endpoint(fmt.Sprintf("/api/v4/projects/%d", mr.SourceID), nil), nil, &source); err != nil {
		return PullRequest{}, fmt.Errorf("gitlab merge request !%d source project %d: %w", mr.IID, mr.SourceID, err)
	}
	pr.HeadRepository = source.PathWithNamespace
	return pr, nil
}

var gitlabCommitStates = map[CommitState]string{CommitStatePending: "pending", CommitStateSuccess: "success",
	CommitStateFailure: "failed", CommitStateError: "failed"}

func (p *gitlabProvider) SetCommitStatus(ctx context.Context, repo RepoURL, sha string, status CommitStatus) error {
	state, err := validateCommitStatus(sha, status, gitlabCommitStates)
	if err != nil {
		return err
	}
	body := status.optionalFields(map[string]any{"state": state, "name": status.Context}, "description", "target_url")
	_, err = p.api.do(ctx, http.MethodPost, p.projectEndpoint(repo, "/statuses/"+url.PathEscape(sha)), body, nil)
	return err
}

// CommentPullRequest adds a merge request note and returns the merge
// request's web_url with the note's #note_<id> anchor (the notes API returns
// no web URL of its own).
func (p *gitlabProvider) CommentPullRequest(ctx context.Context, repo RepoURL, number int, body string) (string, error) {
	if err := validateComment(number, body); err != nil {
		return "", err
	}
	var mr gitlabMergeRequest
	if _, err := p.api.do(ctx, http.MethodGet, p.mrEndpoint(repo, fmt.Sprintf("/%d", number)), nil, &mr); err != nil {
		return "", err
	}
	if mr.WebURL == "" {
		return "", fmt.Errorf("gitlab merge request !%d has no web_url", number)
	}
	var note struct {
		ID int `json:"id"`
	}
	if _, err := p.api.do(ctx, http.MethodPost, p.mrEndpoint(repo, fmt.Sprintf("/%d/notes", number)), map[string]any{"body": body}, &note); err != nil {
		return "", err
	}
	if note.ID == 0 {
		return "", fmt.Errorf("gitlab merge request !%d note created without an id", number)
	}
	return fmt.Sprintf("%s#note_%d", mr.WebURL, note.ID), nil
}
