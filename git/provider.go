package git

import (
	"context"
	"errors"
	"fmt"
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
	ClosePullRequest(ctx context.Context, repo RepoURL, number int) error
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
	Number int
	URL    string
	State  PullRequestState
	Head   string
	Base   string
	Title  string
}

func unsupported(provider, feature string) error {
	return fmt.Errorf("%s: %s is not supported", provider, feature)
}
