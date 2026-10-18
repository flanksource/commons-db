package git

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/flanksource/commons-db/models"
)

const githubAPI = "https://api.github.com"

var linkNextPattern = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// githubProvider is the GitHub (or GitHub Enterprise) REST API v3.
type githubProvider struct {
	api       *restClient
	namespace string
}

type githubRepo struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	SSHURL        string `json:"ssh_url"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
}

type githubPull struct {
	Number   int     `json:"number"`
	HTMLURL  string  `json:"html_url"`
	State    string  `json:"state"`
	Title    string  `json:"title"`
	MergedAt *string `json:"merged_at"`
	Head     struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p *githubProvider) Type() string               { return models.ConnectionTypeGithub }
func (p *githubProvider) SupportsPullRequests() bool { return true }

func (p *githubProvider) Verify(ctx context.Context) error {
	_, err := p.api.do(ctx, http.MethodGet, p.api.endpoint("/user", nil), nil, nil)
	return err
}

// ListRepositories lists an organization's repositories, falling back to the
// user of that name (a namespace is either), or the caller's repositories
// when no namespace is set. Pages follow the Link header.
func (p *githubProvider) ListRepositories(ctx context.Context, options ListRepositoriesOptions) ([]RepositoryInfo, error) {
	namespace := cmp.Or(options.Namespace, p.namespace)
	query := url.Values{"per_page": {"100"}}
	if namespace == "" {
		return p.listPages(ctx, p.api.endpoint("/user/repos", query))
	}
	repos, err := p.listPages(ctx, p.api.endpoint("/orgs/"+url.PathEscape(namespace)+"/repos", query))
	if isStatus(err, http.StatusNotFound) {
		return p.listPages(ctx, p.api.endpoint("/users/"+url.PathEscape(namespace)+"/repos", query))
	}
	return repos, err
}

func (p *githubProvider) listPages(ctx context.Context, next string) ([]RepositoryInfo, error) {
	var repos []RepositoryInfo
	for next != "" {
		var page []githubRepo
		headers, err := p.api.do(ctx, http.MethodGet, next, nil, &page)
		if err != nil {
			return nil, err
		}
		for _, repo := range page {
			namespace, _, _ := strings.Cut(repo.FullName, "/")
			repos = append(repos, RepositoryInfo{Name: repo.Name, FullName: repo.FullName, Namespace: namespace,
				CloneURL: repo.CloneURL, SSHURL: repo.SSHURL, WebURL: repo.HTMLURL, DefaultBranch: repo.DefaultBranch,
				Private: repo.Private, Archived: repo.Archived})
		}
		next = ""
		if match := linkNextPattern.FindStringSubmatch(headers.Get("Link")); match != nil {
			next = match[1]
		}
	}
	return repos, nil
}

func (p *githubProvider) OpenPullRequest(ctx context.Context, repo RepoURL, request PullRequestRequest) (PullRequest, error) {
	if err := request.validate(); err != nil {
		return PullRequest{}, err
	}
	if request.AutoMerge {
		return PullRequest{}, unsupported("github", "auto-merge (it needs the GraphQL API)")
	}
	var pull githubPull
	body := map[string]any{"title": request.Title, "body": request.Body, "head": request.Head, "base": request.Base, "draft": request.Draft}
	if _, err := p.api.do(ctx, http.MethodPost, p.repoEndpoint(repo, "/pulls"), body, &pull); err != nil {
		return PullRequest{}, err
	}
	issue := fmt.Sprintf("/issues/%d", pull.Number)
	for _, extra := range []struct {
		path, field string
		values      []string
	}{
		{issue + "/labels", "labels", request.Labels},
		{issue + "/assignees", "assignees", request.Assignees},
		{fmt.Sprintf("/pulls/%d/requested_reviewers", pull.Number), "reviewers", request.Reviewers},
	} {
		if len(extra.values) == 0 {
			continue
		}
		if _, err := p.api.do(ctx, http.MethodPost, p.repoEndpoint(repo, extra.path), map[string]any{extra.field: extra.values}, nil); err != nil {
			return PullRequest{}, fmt.Errorf("pull request %s opened, but setting %s failed: %w", pull.HTMLURL, extra.field, err)
		}
	}
	return pull.toPullRequest(), nil
}

func (p *githubProvider) GetPullRequest(ctx context.Context, repo RepoURL, number int) (PullRequest, error) {
	var pull githubPull
	if _, err := p.api.do(ctx, http.MethodGet, p.repoEndpoint(repo, fmt.Sprintf("/pulls/%d", number)), nil, &pull); err != nil {
		return PullRequest{}, err
	}
	return pull.toPullRequest(), nil
}

func (p *githubProvider) ClosePullRequest(ctx context.Context, repo RepoURL, number int) error {
	_, err := p.api.do(ctx, http.MethodPatch, p.repoEndpoint(repo, fmt.Sprintf("/pulls/%d", number)), map[string]any{"state": "closed"}, nil)
	return err
}

func (p *githubProvider) repoEndpoint(repo RepoURL, path string) string {
	return p.api.endpoint("/repos/"+url.PathEscape(repo.Owner)+"/"+url.PathEscape(repo.Name)+path, nil)
}

func (pull githubPull) toPullRequest() PullRequest {
	state := PullRequestOpen
	switch {
	case pull.MergedAt != nil:
		state = PullRequestMerged
	case pull.State == "closed":
		state = PullRequestClosed
	}
	return PullRequest{Number: pull.Number, URL: pull.HTMLURL, State: state, Head: pull.Head.Ref, Base: pull.Base.Ref, Title: pull.Title}
}
