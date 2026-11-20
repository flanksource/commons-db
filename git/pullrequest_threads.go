// A pull request's discussion threads: their comments, whether each is resolved, and the file and
// line a review thread is on.
package git

import (
	"context"
	"time"
)

// PullRequestThread is one discussion on a pull request. Path and Line place a review thread on
// a file's line; a general discussion has neither.
type PullRequestThread struct {
	ID       string                     `json:"id"`
	Resolved bool                       `json:"resolved,omitempty"`
	Path     string                     `json:"path,omitempty"`
	Line     int                        `json:"line,omitempty"`
	URL      string                     `json:"url,omitempty"`
	Comments []PullRequestThreadComment `json:"comments"`
}

// PullRequestThreadComment is one comment someone wrote in a thread.
type PullRequestThreadComment struct {
	ID              string    `json:"id"`
	Author          string    `json:"author"`
	AuthorLogin     string    `json:"authorLogin,omitempty"`
	AuthorAvatarURL string    `json:"authorAvatarUrl,omitempty"`
	Body            string    `json:"body"`
	CreatedAt       time.Time `json:"createdAt"`
}

// PullRequestThreadReader reads a pull request's threads that hold a comment someone wrote.
type PullRequestThreadReader interface {
	PullRequestThreads(ctx context.Context, repo RepoURL, number int) ([]PullRequestThread, error)
}
