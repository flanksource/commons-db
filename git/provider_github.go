package git

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

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
	Number    int       `json:"number"`
	HTMLURL   string    `json:"html_url"`
	State     string    `json:"state"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	User      struct {
		Login     string `json:"login"`
		AvatarURL string `json:"avatar_url"`
	} `json:"user"`
	MergedAt       *string `json:"merged_at"`
	Mergeable      *bool   `json:"mergeable"`
	MergeableState string  `json:"mergeable_state"`
	Draft          bool    `json:"draft"`
	Head           struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
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

func (p *githubProvider) ListPullRequestActivity(ctx context.Context, repo RepoURL, number int) ([]PullRequestActivity, error) {
	if number <= 0 {
		return nil, fmt.Errorf("pull request number must be positive")
	}
	activity := []PullRequestActivity{}
	for _, path := range []string{fmt.Sprintf("/issues/%d/comments", number), fmt.Sprintf("/pulls/%d/comments", number)} {
		next := p.repoEndpoint(repo, path) + "?per_page=100"
		for next != "" {
			var comments []struct {
				ID        int64     `json:"id"`
				UpdatedAt time.Time `json:"updated_at"`
				Body      string    `json:"body"`
				HTMLURL   string    `json:"html_url"`
				CreatedAt time.Time `json:"created_at"`
				User      struct {
					Login string `json:"login"`
				} `json:"user"`
			}
			headers, err := p.api.do(ctx, http.MethodGet, next, nil, &comments)
			if err != nil {
				return nil, err
			}
			for _, comment := range comments {
				activity = append(activity, PullRequestActivity{Kind: PullRequestComment, Author: comment.User.Login,
					Body: comment.Body, CreatedAt: comment.CreatedAt, URL: comment.HTMLURL,
					ID: fmt.Sprintf("%s:%d", path, comment.ID), Action: "comment", UpdatedAt: comment.UpdatedAt, Mentions: ActivityMentions(comment.Body)})
			}
			next = ""
			if match := linkNextPattern.FindStringSubmatch(headers.Get("Link")); match != nil {
				next = match[1]
			}
		}
	}
	next := p.repoEndpoint(repo, fmt.Sprintf("/issues/%d/events", number)) + "?per_page=100"
	for next != "" {
		var events []struct {
			ID        int64     `json:"id"`
			Event     string    `json:"event"`
			CreatedAt time.Time `json:"created_at"`
			Actor     struct {
				Login string `json:"login"`
			} `json:"actor"`
		}
		headers, err := p.api.do(ctx, http.MethodGet, next, nil, &events)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			activity = append(activity, PullRequestActivity{Kind: PullRequestHistory, Author: event.Actor.Login,
				Body: strings.ReplaceAll(event.Event, "_", " "), CreatedAt: event.CreatedAt,
				ID: fmt.Sprintf("event:%d", event.ID), Action: event.Event})
		}
		next = ""
		if match := linkNextPattern.FindStringSubmatch(headers.Get("Link")); match != nil {
			next = match[1]
		}
	}
	return activity, nil
}

// ListPullRequests lists by GitHub's open or closed state (closed covers
// merged, told apart by merged_at); a head filter is qualified by repo's owner.
func (p *githubProvider) ListPullRequests(ctx context.Context, repo RepoURL, options ListPullRequestsOptions) ([]PullRequest, error) {
	options, err := options.normalize()
	if err != nil {
		return nil, err
	}
	query := url.Values{"state": {"closed"}}
	if options.State == PullRequestOpen {
		query.Set("state", "open")
	}
	if options.Base != "" {
		query.Set("base", options.Base)
	}
	if options.Head != "" {
		query.Set("head", repo.Owner+":"+options.Head)
	}
	return listPullRequestPages(options, func(page, perPage int) ([]PullRequest, int, error) {
		query.Set("page", strconv.Itoa(page))
		query.Set("per_page", strconv.Itoa(perPage))
		var pulls []githubPull
		if _, err := p.api.do(ctx, http.MethodGet, p.repoEndpoint(repo, "/pulls")+"?"+query.Encode(), nil, &pulls); err != nil {
			return nil, 0, err
		}
		converted := make([]PullRequest, 0, len(pulls))
		for _, pull := range pulls {
			converted = append(converted, pull.toPullRequest())
		}
		return converted, len(pulls), nil
	})
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
	pr := PullRequest{Number: pull.Number, URL: pull.HTMLURL, State: state, Head: pull.Head.Ref, Base: pull.Base.Ref,
		Title: pull.Title, Author: pull.User.Login, Body: pull.Body, CreatedAt: pull.CreatedAt, HeadSHA: pull.Head.SHA}
	if pull.Head.Repo != nil {
		pr.HeadRepository = pull.Head.Repo.FullName
	}
	if pull.User.AvatarURL != "" || pull.MergeableState != "" || pull.Mergeable != nil || pull.Base.SHA != "" || pull.Draft {
		pr.Metadata = &PullRequestMetadata{AuthorAvatarURL: pull.User.AvatarURL, BaseSHA: pull.Base.SHA,
			Mergeability: githubMergeability(pull.Mergeable, pull.MergeableState), MergeReason: pull.MergeableState, Draft: pull.Draft}
	}
	return pr
}

var githubCommitStates = map[CommitState]string{CommitStatePending: "pending", CommitStateSuccess: "success",
	CommitStateFailure: "failure", CommitStateError: "error"}

func (p *githubProvider) SetCommitStatus(ctx context.Context, repo RepoURL, sha string, status CommitStatus) error {
	state, err := validateCommitStatus(sha, status, githubCommitStates)
	if err != nil {
		return err
	}
	body := status.optionalFields(map[string]any{"state": state, "context": status.Context}, "description", "target_url")
	_, err = p.api.do(ctx, http.MethodPost, p.repoEndpoint(repo, "/statuses/"+url.PathEscape(sha)), body, nil)
	return err
}

// CommentPullRequest adds an issue comment (the pull request's conversation)
// and returns its html_url.
func (p *githubProvider) CommentPullRequest(ctx context.Context, repo RepoURL, number int, body string) (string, error) {
	if err := validateComment(number, body); err != nil {
		return "", err
	}
	var comment struct {
		HTMLURL string `json:"html_url"`
	}
	if _, err := p.api.do(ctx, http.MethodPost, p.repoEndpoint(repo, fmt.Sprintf("/issues/%d/comments", number)),
		map[string]any{"body": body}, &comment); err != nil {
		return "", err
	}
	if comment.HTMLURL == "" {
		return "", fmt.Errorf("github pull request %d comment created without an html_url", number)
	}
	return comment.HTMLURL, nil
}
