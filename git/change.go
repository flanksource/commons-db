package git

import (
	"context"
	"errors"
	"fmt"
)

// FileChange writes Content to Path, or deletes Path when Delete is set.
type FileChange struct {
	Path    string
	Content []byte
	Delete  bool
}

// ChangeRequest commits Files on a new Branch cut from Base, pushes it and,
// optionally, tags the commit and opens a pull request into Base.
//
// BaseSHA, when set, pins the change to the commit the caller verified Files
// against: the branch is cut from exactly that commit, and a Base whose head
// is anywhere else fails with ErrBaseMoved before anything is pushed.
type ChangeRequest struct {
	Base        string
	BaseSHA     string
	Branch      string
	Message     string
	Author      Signature
	Files       []FileChange
	PullRequest *PullRequestRequest
	Tag         *TagOptions
}

// ChangeResult is what Apply pushed and opened.
type ChangeResult struct {
	Branch      string
	CommitSHA   string
	PullRequest *PullRequest
	Tag         string
}

// Apply runs a ChangeRequest. Every check that can fail without touching the
// remote runs first: a provider without pull requests returns
// ErrPullRequestsUnsupported and an existing remote branch ErrBranchExists,
// before anything is pushed.
func (c *Cache) Apply(ctx context.Context, repo *Repository, request ChangeRequest) (ChangeResult, error) {
	base, err := c.preflight(ctx, repo, request)
	if err != nil {
		return ChangeResult{}, err
	}
	worktree, err := c.checkoutRef(ctx, repo, base, CheckoutOptions{Ephemeral: true})
	if err != nil {
		return ChangeResult{}, err
	}
	if request.BaseSHA != "" && worktree.Ref.SHA != request.BaseSHA {
		return ChangeResult{}, errors.Join(baseMoved(request, worktree.Ref.SHA), worktree.Release())
	}
	result, err := applyIn(ctx, worktree, request)
	if err := errors.Join(err, worktree.Release()); err != nil {
		return result, err
	}
	if request.PullRequest == nil {
		return result, nil
	}
	pullRequest := *request.PullRequest
	pullRequest.Head, pullRequest.Base = request.Branch, base.Name
	opened, err := repo.Provider.OpenPullRequest(ctx, repo.URL, pullRequest)
	if err != nil {
		return result, fmt.Errorf("branch %s pushed, but opening the pull request failed: %w", request.Branch, err)
	}
	result.PullRequest = &opened
	return result, nil
}

func (c *Cache) preflight(ctx context.Context, repo *Repository, request ChangeRequest) (Ref, error) {
	if err := request.validate(); err != nil {
		return Ref{}, err
	}
	if request.PullRequest != nil && (repo.Provider == nil || !repo.Provider.SupportsPullRequests()) {
		return Ref{}, ErrPullRequestsUnsupported
	}
	refs, err := repo.LsRemote(ctx)
	if err != nil {
		return Ref{}, err
	}
	base, err := refs.Resolve(request.Base)
	if err != nil {
		return Ref{}, fmt.Errorf("change base: %w", err)
	}
	if request.PullRequest != nil && base.Kind != RefBranch {
		return Ref{}, fmt.Errorf("pull request base %q must be a branch, it is a %s", request.Base, base.Kind)
	}
	if request.BaseSHA != "" && base.SHA != request.BaseSHA {
		return Ref{}, baseMoved(request, base.SHA)
	}
	if _, exists := refs.findIn(refs.Branches, request.Branch); exists {
		return Ref{}, fmt.Errorf("%w: %s", ErrBranchExists, request.Branch)
	}
	if request.Tag != nil {
		if _, exists := refs.findIn(refs.Tags, request.Tag.Name); exists {
			return Ref{}, fmt.Errorf("%w: %s", ErrTagExists, request.Tag.Name)
		}
	}
	return base, nil
}

func (r ChangeRequest) validate() error {
	if err := ValidateBranchName(r.Branch); err != nil {
		return err
	}
	if r.Base == "" {
		return errors.New("change base is required")
	}
	if r.BaseSHA != "" && !fullSHAPattern.MatchString(r.BaseSHA) {
		return fmt.Errorf("change base SHA %q must be a full 40-character lowercase commit SHA", r.BaseSHA)
	}
	if len(r.Files) == 0 {
		return errors.New("change has no files")
	}
	if r.Tag != nil {
		return ValidateTagName(r.Tag.Name)
	}
	return nil
}

func baseMoved(request ChangeRequest, actual string) error {
	return fmt.Errorf("%w: %s is at %s, expected %s", ErrBaseMoved, request.Base, actual, request.BaseSHA)
}

func applyIn(ctx context.Context, worktree *Worktree, request ChangeRequest) (ChangeResult, error) {
	if err := worktree.CreateBranch(ctx, request.Branch); err != nil {
		return ChangeResult{}, err
	}
	for _, file := range request.Files {
		write := func() error { return worktree.WriteFile(file.Path, file.Content) }
		if file.Delete {
			write = func() error { return worktree.DeleteFile(file.Path) }
		}
		if err := write(); err != nil {
			return ChangeResult{}, err
		}
	}
	sha, err := worktree.Commit(ctx, CommitOptions{Message: request.Message, Author: request.Author})
	if err != nil {
		return ChangeResult{}, err
	}
	push := PushOptions{Branch: request.Branch}
	result := ChangeResult{Branch: request.Branch, CommitSHA: sha}
	if request.Tag != nil {
		tag := *request.Tag
		tag.Tagger = request.Author
		if err := worktree.CreateTag(ctx, tag); err != nil {
			return ChangeResult{}, err
		}
		push.Tags, result.Tag = []string{tag.Name}, tag.Name
	}
	if err := worktree.Push(ctx, push); err != nil {
		return ChangeResult{}, err
	}
	return result, nil
}
