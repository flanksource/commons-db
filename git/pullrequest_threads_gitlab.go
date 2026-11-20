// GitLab merge request discussions as threads: their notes without GitLab's system ones, whether a
// resolvable one is resolved, and the file line a diff note is on; and opening, replying in,
// resolving and reopening one.
package git

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type gitlabDiscussion struct {
	ID    string       `json:"id"`
	Notes []gitlabNote `json:"notes"`
}

type gitlabNote struct {
	ID         int64     `json:"id"`
	Body       string    `json:"body"`
	System     bool      `json:"system"`
	Resolvable bool      `json:"resolvable"`
	Resolved   bool      `json:"resolved"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Author     struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Username  string `json:"username"`
		AvatarURL string `json:"avatar_url"`
	} `json:"author"`
	Position *struct {
		NewPath string `json:"new_path"`
		NewLine int    `json:"new_line"`
		OldPath string `json:"old_path"`
		OldLine int    `json:"old_line"`
	} `json:"position"`
}

// comment is note as a thread comment, replying to parent unless it is "".
func (note gitlabNote) comment(parent string) PullRequestThreadComment {
	comment := PullRequestThreadComment{ID: strconv.FormatInt(note.ID, 10), ParentID: parent, Author: note.Author.Name,
		AuthorLogin: note.Author.Username, AuthorAvatarURL: note.Author.AvatarURL, Body: note.Body, CreatedAt: note.CreatedAt}
	if note.Author.ID != 0 {
		comment.AuthorID = strconv.FormatInt(note.Author.ID, 10)
	}
	return comment
}

// thread is discussion on merge request number of repo as a thread; GitLab keeps a discussion's
// notes flat, so each reply's parent is its first note.
func (discussion gitlabDiscussion) thread(repo RepoURL, number int) PullRequestThread {
	thread := PullRequestThread{ID: discussion.ID}
	for _, note := range discussion.Notes {
		if note.System {
			continue
		}
		if len(thread.Comments) == 0 {
			thread.URL = fmt.Sprintf("%s/-/merge_requests/%d#note_%d", repo.webBase(), number, note.ID)
			if note.Resolvable {
				thread.Resolved, thread.Status = note.Resolved, "unresolved"
				if note.Resolved {
					thread.Status = "resolved"
				}
			}
			if position := note.Position; position != nil {
				thread.Path, thread.Line = position.NewPath, position.NewLine
				if position.NewLine == 0 {
					thread.Path, thread.Line = position.OldPath, position.OldLine
				}
			}
		}
		parent := ""
		if len(thread.Comments) > 0 {
			parent = thread.Comments[0].ID
		}
		thread.Comments = append(thread.Comments, note.comment(parent))
		if note.UpdatedAt.After(thread.UpdatedAt) {
			thread.UpdatedAt = note.UpdatedAt
		}
	}
	return thread
}

// PullRequestThreads reads the merge request's discussions someone wrote in.
func (p *gitlabProvider) PullRequestThreads(ctx context.Context, repo RepoURL, number int) ([]PullRequestThread, error) {
	var discussions []gitlabDiscussion
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.mrEndpoint(repo, fmt.Sprintf("/%d/discussions", number)) + "?per_page=100", Out: &discussions}); err != nil {
		return nil, err
	}
	var threads []PullRequestThread
	for _, discussion := range discussions {
		if thread := discussion.thread(repo, number); len(thread.Comments) > 0 {
			threads = append(threads, thread)
		}
	}
	return threads, nil
}

func (p *gitlabProvider) PullRequestThread(ctx context.Context, repo RepoURL, number int, thread string) (PullRequestThread, error) {
	var discussion gitlabDiscussion
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.discussionEndpoint(repo, number, thread, ""), Out: &discussion}); err != nil {
		return PullRequestThread{}, err
	}
	return discussion.thread(repo, number), nil
}

// CreatePullRequestThread opens a discussion; one on a file's lines goes on its last line, as
// GitLab places a diff note on one line, of the diff the merge request is at.
func (p *gitlabProvider) CreatePullRequestThread(ctx context.Context, repo RepoURL, number int, request PullRequestThreadRequest) (PullRequestThread, error) {
	body := map[string]any{"body": request.Body}
	if request.Path != "" {
		var mr struct {
			DiffRefs struct {
				BaseSHA  string `json:"base_sha"`
				HeadSHA  string `json:"head_sha"`
				StartSHA string `json:"start_sha"`
			} `json:"diff_refs"`
		}
		if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.mrEndpoint(repo, fmt.Sprintf("/%d", number)), Out: &mr}); err != nil {
			return PullRequestThread{}, err
		}
		if mr.DiffRefs.HeadSHA != request.HeadSHA {
			return PullRequestThread{}, fmt.Errorf("gitlab merge request !%d is at %s, not %s: %w", number, mr.DiffRefs.HeadSHA, request.HeadSHA, ErrThreadTargetMoved)
		}
		path := strings.TrimPrefix(request.Path, "/")
		position := map[string]any{"position_type": "text", "base_sha": mr.DiffRefs.BaseSHA, "head_sha": mr.DiffRefs.HeadSHA,
			"start_sha": mr.DiffRefs.StartSHA, "old_path": path, "new_path": path}
		if request.Old {
			position["old_line"] = request.EndLine
		} else {
			position["new_line"] = request.EndLine
		}
		body["position"] = position
	}
	var discussion gitlabDiscussion
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodPost, Endpoint: p.mrEndpoint(repo, fmt.Sprintf("/%d/discussions", number)), Body: body, Out: &discussion}); err != nil {
		return PullRequestThread{}, err
	}
	return discussion.thread(repo, number), nil
}

// ReplyPullRequestThread adds a note to the discussion; GitLab keeps its notes flat, so the reply
// is under parent only as this answers it.
func (p *gitlabProvider) ReplyPullRequestThread(ctx context.Context, repo RepoURL, number int, thread, parent, body string) (PullRequestThreadComment, error) {
	var note gitlabNote
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodPost, Endpoint: p.discussionEndpoint(repo, number, thread, "/notes"), Body: map[string]any{"body": body}, Out: &note}); err != nil {
		return PullRequestThreadComment{}, err
	}
	return note.comment(parent), nil
}

func (p *gitlabProvider) ResolvePullRequestThread(ctx context.Context, repo RepoURL, number int, thread string, resolved bool) (PullRequestThread, error) {
	query := url.Values{"resolved": {strconv.FormatBool(resolved)}}
	var discussion gitlabDiscussion
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodPut, Endpoint: p.discussionEndpoint(repo, number, thread, "") + "?" + query.Encode(), Out: &discussion}); err != nil {
		return PullRequestThread{}, err
	}
	left := discussion.thread(repo, number)
	if left.Resolved != resolved {
		want, state := "resolving", "unresolved"
		if !resolved {
			want, state = "reopening", "resolved"
		}
		return PullRequestThread{}, fmt.Errorf("gitlab left merge request !%d discussion %s %s instead of %s it", number, thread, state, want)
	}
	return left, nil
}

func (p *gitlabProvider) discussionEndpoint(repo RepoURL, number int, thread, suffix string) string {
	return p.mrEndpoint(repo, fmt.Sprintf("/%d/discussions/%s%s", number, url.PathEscape(thread), suffix))
}
