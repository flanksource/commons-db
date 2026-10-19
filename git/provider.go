package git

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrPullRequestsUnsupported is returned when the repository's provider
	// cannot open pull requests (plain git, or no provider).
	ErrPullRequestsUnsupported = errors.New("pull requests are not supported by this git provider")
	// ErrBranchExists is returned when a change would push to a branch that
	// already exists on the remote.
	ErrBranchExists = errors.New("branch already exists on the remote")
	// ErrTagExists is returned when a change would push a tag that already
	// exists on the remote.
	ErrTagExists = errors.New("tag already exists on the remote")
	// ErrBaseMoved is returned when a change pinned to a base commit
	// (ChangeRequest.BaseSHA) finds the base branch at a different commit.
	ErrBaseMoved = errors.New("change base moved off the expected commit")
)

// Provider is a git hosting provider's REST API.
type Provider interface {
	// Type is the models.ConnectionType* of the provider.
	Type() string
	// Verify checks the credentials against the provider.
	Verify(ctx context.Context) error
	ListRepositories(ctx context.Context, options ListRepositoriesOptions) ([]RepositoryInfo, error)
	// SupportsPullRequests reports whether OpenPullRequest can succeed, so a
	// change can be refused before anything is pushed.
	SupportsPullRequests() bool
	OpenPullRequest(ctx context.Context, repo RepoURL, request PullRequestRequest) (PullRequest, error)
	GetPullRequest(ctx context.Context, repo RepoURL, number int) (PullRequest, error)
	ListPullRequestActivity(ctx context.Context, repo RepoURL, number int) ([]PullRequestActivity, error)
	ListPullRequests(ctx context.Context, repo RepoURL, options ListPullRequestsOptions) ([]PullRequest, error)
	ClosePullRequest(ctx context.Context, repo RepoURL, number int) error
	// SetCommitStatus reports a commit status (a check) on sha.
	SetCommitStatus(ctx context.Context, repo RepoURL, sha string, status CommitStatus) error
	// CommentPullRequest adds a comment to the pull request and returns the
	// comment's web URL.
	CommentPullRequest(ctx context.Context, repo RepoURL, number int, body string) (string, error)
}

// ListRepositoriesOptions scopes ListRepositories. Namespace is a GitHub
// owner, GitLab group or Azure DevOps project; empty uses the connection's
// configured namespace, else every repository the credentials can see.
type ListRepositoriesOptions struct {
	Namespace string
}

// RepositoryInfo is one repository as the provider lists it.
type RepositoryInfo struct {
	Name          string
	FullName      string
	Namespace     string
	CloneURL      string
	SSHURL        string
	WebURL        string
	DefaultBranch string
	Private       bool
	Archived      bool
}

// PullRequestRequest opens a pull request from Head into Base.
type PullRequestRequest struct {
	Title     string
	Body      string
	Head      string
	Base      string
	Draft     bool
	Labels    []string
	Assignees []string
	Reviewers []string
	AutoMerge bool
}

func (r PullRequestRequest) validate() error {
	if r.Title == "" || r.Head == "" || r.Base == "" {
		return fmt.Errorf("pull request needs a title, a head branch and a base branch")
	}
	return nil
}

// PullRequestState is a provider-neutral pull request state.
type PullRequestState string

const (
	PullRequestOpen   PullRequestState = "open"
	PullRequestClosed PullRequestState = "closed"
	PullRequestMerged PullRequestState = "merged"
)

// PullRequest is an opened pull (or merge) request.
type PullRequest struct {
	Number    int
	URL       string
	State     PullRequestState
	Head      string
	Base      string
	Title     string
	Author    string
	Body      string
	CreatedAt time.Time
	// HeadSHA is the commit the head branch points at.
	HeadSHA string
	// HeadRepository is the full name (as RepositoryInfo.FullName) of the
	// repository the head branch lives in: the fork for a fork pull request,
	// the pull request's own repository otherwise. Empty only when the
	// provider no longer knows it (GitHub: a deleted fork).
	HeadRepository string
}

type PullRequestActivityKind string

const (
	PullRequestComment PullRequestActivityKind = "comment"
	PullRequestHistory PullRequestActivityKind = "history"
)

type PullRequestActivity struct {
	Kind      PullRequestActivityKind `json:"kind"`
	Author    string                  `json:"author"`
	Body      string                  `json:"body"`
	CreatedAt time.Time               `json:"createdAt"`
	URL       string                  `json:"url,omitempty"`
}

const (
	defaultPullRequestLimit = 100
	maxPullRequestPageSize  = 100
)

// ListPullRequestsOptions filters ListPullRequests.
type ListPullRequestsOptions struct {
	// Base is the branch the pull requests merge into; empty matches any.
	Base string
	// Head is the branch the pull requests merge from; empty matches any.
	Head string
	// State is the state to list; the zero value lists open pull requests.
	State PullRequestState
	// Limit caps how many pull requests are returned, following pages until
	// it is reached; <= 0 means 100.
	Limit int
}

func (o ListPullRequestsOptions) normalize() (ListPullRequestsOptions, error) {
	o.State = cmp.Or(o.State, PullRequestOpen)
	if o.State != PullRequestOpen && o.State != PullRequestClosed && o.State != PullRequestMerged {
		return o, fmt.Errorf("list pull requests: unknown pull request state %q (want open, closed or merged)", o.State)
	}
	if o.Limit <= 0 {
		o.Limit = defaultPullRequestLimit
	}
	return o, nil
}

// pullRequestPage fetches the 1-based page of perPage pull requests and
// returns them with the number of entries the provider returned.
type pullRequestPage func(page, perPage int) ([]PullRequest, int, error)

// listPullRequestPages keeps the pull requests in options.State from
// successive pages until options.Limit are kept or a page comes back short.
func listPullRequestPages(options ListPullRequestsOptions, fetch pullRequestPage) ([]PullRequest, error) {
	perPage := min(options.Limit, maxPullRequestPageSize)
	var pulls []PullRequest
	for page := 1; ; page++ {
		got, returned, err := fetch(page, perPage)
		if err != nil {
			return nil, err
		}
		for _, pull := range got {
			if pull.State == options.State {
				pulls = append(pulls, pull)
			}
		}
		if len(pulls) >= options.Limit {
			return pulls[:options.Limit], nil
		}
		if returned < perPage {
			return pulls, nil
		}
	}
}

// CommitState is a provider-neutral commit status state.
type CommitState string

const (
	CommitStatePending CommitState = "pending"
	CommitStateSuccess CommitState = "success"
	CommitStateFailure CommitState = "failure"
	CommitStateError   CommitState = "error"
)

// CommitStatus is a status reported on a commit. Context names the check
// (statuses with the same context replace each other); TargetURL links to
// its details.
type CommitStatus struct {
	State       CommitState
	Context     string
	Description string
	TargetURL   string
}

// validateCommitStatus maps status.State through states (the provider's
// names for the neutral states) after checking sha and context.
func validateCommitStatus(sha string, status CommitStatus, states map[CommitState]string) (string, error) {
	if strings.TrimSpace(sha) == "" {
		return "", fmt.Errorf("commit status needs a commit sha")
	}
	if strings.TrimSpace(status.Context) == "" {
		return "", fmt.Errorf("commit status on %s needs a context", sha)
	}
	state, ok := states[status.State]
	if !ok {
		return "", fmt.Errorf("commit status on %s: unknown commit state %q (want pending, success, failure or error)", sha, status.State)
	}
	return state, nil
}

// optionalFields adds Description and TargetURL to body under the provider's
// field names, leaving out the empty ones.
func (s CommitStatus) optionalFields(body map[string]any, descriptionField, targetURLField string) map[string]any {
	if s.Description != "" {
		body[descriptionField] = s.Description
	}
	if s.TargetURL != "" {
		body[targetURLField] = s.TargetURL
	}
	return body
}

func validateComment(number int, body string) error {
	if number <= 0 {
		return fmt.Errorf("pull request comment needs a pull request number, got %d", number)
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("pull request %d comment needs a comment body", number)
	}
	return nil
}

func unsupported(provider, feature string) error {
	return fmt.Errorf("%s: %s is not supported", provider, feature)
}
