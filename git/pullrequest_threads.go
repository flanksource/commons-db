// A pull request's discussion threads: their comments, whether each is resolved, and the file and
// line a review thread is on; and writing to them: opening, replying, resolving and reopening.
package git

import (
	"context"
	"errors"
	"time"
)

// ErrThreadTargetMoved refuses a thread on a file's lines of a diff the pull request has moved
// past: its head, or the iteration of it, is no longer the one the lines were read from.
var ErrThreadTargetMoved = errors.New("the pull request moved past the diff the thread's lines are in")

// PullRequestThread is one discussion on a pull request. Status is the provider's own name for its
// state; Path and Line place a review thread on a file's line, and a general discussion has neither.
type PullRequestThread struct {
	ID        string                     `json:"id"`
	Resolved  bool                       `json:"resolved,omitempty"`
	Status    string                     `json:"status,omitempty"`
	Path      string                     `json:"path,omitempty"`
	Line      int                        `json:"line,omitempty"`
	URL       string                     `json:"url,omitempty"`
	UpdatedAt time.Time                  `json:"updatedAt,omitempty"`
	Comments  []PullRequestThreadComment `json:"comments"`
}

// PullRequestThreadComment is one comment someone wrote in a thread, under ParentID's when it
// replies to one.
type PullRequestThreadComment struct {
	ID              string    `json:"id"`
	ParentID        string    `json:"parentId,omitempty"`
	Author          string    `json:"author"`
	AuthorID        string    `json:"authorId,omitempty"`
	AuthorLogin     string    `json:"authorLogin,omitempty"`
	AuthorAvatarURL string    `json:"authorAvatarUrl,omitempty"`
	Body            string    `json:"body"`
	CreatedAt       time.Time `json:"createdAt"`
}

// PullRequestThreadReader reads a pull request's threads that hold a comment someone wrote.
type PullRequestThreadReader interface {
	PullRequestThreads(ctx context.Context, repo RepoURL, number int) ([]PullRequestThread, error)
}

// PullRequestThreadRequest opens a thread with Body: on lines StartLine to EndLine of Path in the
// diff from BaseSHA to HeadSHA when Path is set, on its old side when Old, else a general one.
type PullRequestThreadRequest struct {
	Body      string
	Path      string
	StartLine int
	EndLine   int
	Old       bool
	HeadSHA   string
	BaseSHA   string
}

// PullRequestThreadWriter reads one thread, opens one, replies in one under a comment of it, and
// resolves or reopens one, each answered with the thread or comment as the provider left it.
type PullRequestThreadWriter interface {
	PullRequestThread(ctx context.Context, repo RepoURL, number int, thread string) (PullRequestThread, error)
	CreatePullRequestThread(ctx context.Context, repo RepoURL, number int, request PullRequestThreadRequest) (PullRequestThread, error)
	ReplyPullRequestThread(ctx context.Context, repo RepoURL, number int, thread, parent, body string) (PullRequestThreadComment, error)
	ResolvePullRequestThread(ctx context.Context, repo RepoURL, number int, thread string, resolved bool) (PullRequestThread, error)
}
