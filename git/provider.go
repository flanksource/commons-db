package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	Number int
	URL    string
	State  PullRequestState
	Head   string
	Base   string
	Title  string
	// HeadSHA is the commit the head branch points at.
	HeadSHA string
	// HeadRepository is the full name (as RepositoryInfo.FullName) of the
	// repository the head branch lives in: the fork for a fork pull request,
	// the pull request's own repository otherwise. Empty only when the
	// provider no longer knows it (GitHub: a deleted fork).
	HeadRepository string
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
