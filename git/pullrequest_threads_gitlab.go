// GitLab merge request discussions as threads: their notes without GitLab's system ones, whether a
// resolvable one is resolved, and the file line a diff note is on; and opening one on a file's
// lines of the diff, replying in, resolving and reopening one.
package git

import (
	"context"
	"crypto/sha1"
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

// CreatePullRequestThread opens a discussion; one on a file's lines goes on them in the diff the
// merge request is at, each end of a range named by its line code.
func (p *gitlabProvider) CreatePullRequestThread(ctx context.Context, repo RepoURL, number int, request PullRequestThreadRequest) (PullRequestThread, error) {
	body := map[string]any{"body": request.Body}
	if request.Path != "" {
		position, err := p.diffPosition(ctx, repo, number, request)
		if err != nil {
			return PullRequestThread{}, err
		}
		body["position"] = position
	}
	var discussion gitlabDiscussion
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodPost, Endpoint: p.mrEndpoint(repo, fmt.Sprintf("/%d/discussions", number)), Body: body, Out: &discussion}); err != nil {
		return PullRequestThread{}, err
	}
	return discussion.thread(repo, number), nil
}

// diffPosition is where request's note goes in the diff merge request number is at: its last
// line, and with a range of lines, the line_range from its first.
func (p *gitlabProvider) diffPosition(ctx context.Context, repo RepoURL, number int, request PullRequestThreadRequest) (map[string]any, error) {
	var mr struct {
		DiffRefs struct {
			BaseSHA  string `json:"base_sha"`
			HeadSHA  string `json:"head_sha"`
			StartSHA string `json:"start_sha"`
		} `json:"diff_refs"`
	}
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.mrEndpoint(repo, fmt.Sprintf("/%d", number)), Out: &mr}); err != nil {
		return nil, err
	}
	if mr.DiffRefs.HeadSHA != request.HeadSHA {
		return nil, fmt.Errorf("gitlab merge request !%d is at %s, not %s: %w", number, mr.DiffRefs.HeadSHA, request.HeadSHA, ErrThreadTargetMoved)
	}
	path := strings.TrimPrefix(request.Path, "/")
	file, err := p.diffFile(ctx, repo, number, path)
	if err != nil {
		return nil, err
	}
	lines, err := gitlabDiffLines(file.Diff)
	if err != nil {
		return nil, fmt.Errorf("gitlab merge request !%d's diff of %s: %w", number, path, err)
	}
	find := func(want int) (gitlabDiffLine, error) {
		for _, line := range lines {
			if line.on(request.Old) == want {
				return line, nil
			}
		}
		side := "new"
		if request.Old {
			side = "old"
		}
		return gitlabDiffLine{}, fmt.Errorf("gitlab merge request !%d's diff of %s doesn't show %s line %d", number, path, side, want)
	}
	end, err := find(request.EndLine)
	if err != nil {
		return nil, err
	}
	position := map[string]any{"position_type": "text", "base_sha": mr.DiffRefs.BaseSHA, "head_sha": mr.DiffRefs.HeadSHA,
		"start_sha": mr.DiffRefs.StartSHA, "old_path": file.OldPath, "new_path": file.NewPath}
	end.place(position)
	if request.StartLine != 0 && request.StartLine < request.EndLine {
		start, err := find(request.StartLine)
		if err != nil {
			return nil, err
		}
		position["line_range"] = map[string]any{"start": start.rangeEnd(file.NewPath), "end": end.rangeEnd(file.NewPath)}
	}
	return position, nil
}

type gitlabDiffFile struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Diff    string `json:"diff"`
}

// diffFile is the change merge request number makes to the file at path, by its new or old path,
// paging its diffs by X-Next-Page.
func (p *gitlabProvider) diffFile(ctx context.Context, repo RepoURL, number int, path string) (gitlabDiffFile, error) {
	query := url.Values{"per_page": {"100"}}
	for {
		var files []gitlabDiffFile
		headers, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.mrEndpoint(repo, fmt.Sprintf("/%d/diffs", number)) + "?" + query.Encode(), Out: &files})
		if err != nil {
			return gitlabDiffFile{}, err
		}
		for _, file := range files {
			if file.NewPath == path || file.OldPath == path {
				return file, nil
			}
		}
		page := strings.TrimSpace(headers.Get("X-Next-Page"))
		if page == "" {
			return gitlabDiffFile{}, fmt.Errorf("gitlab merge request !%d doesn't change %s", number, path)
		}
		query.Set("page", page)
	}
}

// gitlabDiffLine is a line of a unified diff with the old and new line numbers GitLab gives it:
// an added line carries the old line after it, a removed one the new line after it.
type gitlabDiffLine struct {
	Kind     byte
	Old, New int
}

// on is the line's number on the old or new side, or 0 when it isn't on that side.
func (line gitlabDiffLine) on(old bool) int {
	switch {
	case old && line.Kind != '+':
		return line.Old
	case !old && line.Kind != '-':
		return line.New
	}
	return 0
}

// place sets the line numbers GitLab places a note on line by: an added line's new one, a removed
// line's old one, and both of an unchanged one.
func (line gitlabDiffLine) place(position map[string]any) {
	if line.Kind != '+' {
		position["old_line"] = line.Old
	}
	if line.Kind != '-' {
		position["new_line"] = line.New
	}
}

// rangeEnd is line as an end of a line_range in the diff of the file at path: its line code, the
// SHA-1 of path with its old and new line, and "new" for an added line, else "old".
func (line gitlabDiffLine) rangeEnd(path string) map[string]any {
	end := map[string]any{"line_code": fmt.Sprintf("%x_%d_%d", sha1.Sum([]byte(path)), line.Old, line.New), "type": "old"}
	if line.Kind == '+' {
		end["type"] = "new"
	}
	line.place(end)
	return end
}

// gitlabDiffLines are the lines of diff's hunks, numbered as GitLab numbers them.
func gitlabDiffLines(diff string) ([]gitlabDiffLine, error) {
	var lines []gitlabDiffLine
	old, current := 0, 0
	for _, text := range strings.Split(diff, "\n") {
		if strings.HasPrefix(text, "@@") {
			if _, err := fmt.Sscanf(text, "@@ -%d", &old); err != nil {
				return nil, fmt.Errorf("hunk header %q: %w", text, err)
			}
			_, added, found := strings.Cut(text, " +")
			if !found {
				return nil, fmt.Errorf("hunk header %q names no new lines", text)
			}
			if _, err := fmt.Sscanf(added, "%d", &current); err != nil {
				return nil, fmt.Errorf("hunk header %q: %w", text, err)
			}
			continue
		}
		if text == "" || old == 0 && current == 0 {
			continue
		}
		switch kind := text[0]; kind {
		case '+':
			lines = append(lines, gitlabDiffLine{Kind: kind, Old: old, New: current})
			current++
		case '-':
			lines = append(lines, gitlabDiffLine{Kind: kind, Old: old, New: current})
			old++
		case ' ':
			lines = append(lines, gitlabDiffLine{Kind: kind, Old: old, New: current})
			old++
			current++
		}
	}
	return lines, nil
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
