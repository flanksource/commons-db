// Azure DevOps pull request threads: each live thread's text comments, its status read as
// resolved or not, the file and line its context places it on; and opening, replying, resolving.
package git

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type azureThreadComment struct {
	ID       int64     `json:"id"`
	ParentID int64     `json:"parentCommentId"`
	Body     string    `json:"content"`
	Kind     string    `json:"commentType"`
	Deleted  bool      `json:"isDeleted"`
	Created  time.Time `json:"publishedDate"`
	Author   struct {
		ID     string `json:"id"`
		Name   string `json:"displayName"`
		Login  string `json:"uniqueName"`
		Avatar string `json:"imageUrl"`
	} `json:"author"`
}

type azureThread struct {
	ID        int             `json:"id"`
	Status    json.RawMessage `json:"status"`
	Deleted   bool            `json:"isDeleted"`
	UpdatedAt time.Time       `json:"lastUpdatedDate"`
	Context   *struct {
		Path  string `json:"filePath"`
		Right *struct {
			Line int `json:"line"`
		} `json:"rightFileStart"`
		Left *struct {
			Line int `json:"line"`
		} `json:"leftFileStart"`
	} `json:"threadContext"`
	Comments []azureThreadComment `json:"comments"`
}

// azureThreadStatuses names the statuses Azure sends as numbers, and says which are resolved.
var azureThreadStatuses = map[string]struct {
	name     string
	resolved bool
}{
	"1": {"active", false}, "2": {"fixed", true}, "3": {"wontFix", true}, "4": {"closed", true}, "5": {"byDesign", true}, "6": {"pending", false},
}

// azureThreadStatus reads a thread status, which Azure sends as a name or its number.
func azureThreadStatus(status json.RawMessage) (string, bool, bool) {
	raw := strings.Trim(string(status), `"`)
	if known, ok := azureThreadStatuses[raw]; ok {
		return known.name, known.resolved, true
	}
	for _, known := range azureThreadStatuses {
		if known.name == raw {
			return known.name, known.resolved, true
		}
	}
	return "", false, false
}

func (c azureThreadComment) read() PullRequestThreadComment {
	comment := PullRequestThreadComment{ID: strconv.FormatInt(c.ID, 10), Author: c.Author.Name, AuthorID: c.Author.ID,
		AuthorLogin: c.Author.Login, AuthorAvatarURL: c.Author.Avatar, Body: c.Body, CreatedAt: c.Created}
	if c.ParentID > 0 {
		comment.ParentID = strconv.FormatInt(c.ParentID, 10)
	}
	return comment
}

// read is the thread with its live text comments; ok is false for one that has none to read.
func (t azureThread) read(repo RepoURL, number int) (PullRequestThread, bool, error) {
	thread := PullRequestThread{ID: strconv.Itoa(t.ID), UpdatedAt: t.UpdatedAt,
		URL: fmt.Sprintf("%s/pullrequest/%d?discussionId=%d", repo.azureWebBase(), number, t.ID)}
	for _, comment := range t.Comments {
		if !comment.Deleted && comment.Kind == "text" && comment.Body != "" {
			thread.Comments = append(thread.Comments, comment.read())
		}
	}
	if t.Deleted || len(thread.Comments) == 0 {
		return thread, false, nil
	}
	status, resolved, known := azureThreadStatus(t.Status)
	if !known {
		return thread, false, fmt.Errorf("azure DevOps pull request %d thread %d has unknown status %s", number, t.ID, t.Status)
	}
	thread.Status, thread.Resolved = status, resolved
	if t.Context != nil {
		thread.Path = t.Context.Path
		switch {
		case t.Context.Right != nil:
			thread.Line = t.Context.Right.Line
		case t.Context.Left != nil:
			thread.Line = t.Context.Left.Line
		}
	}
	return thread, true, nil
}

func (p *azureProvider) threadsEndpoint(repo RepoURL, number int, suffix string) string {
	return p.pullEndpoint(repo, fmt.Sprintf("/%d/threads%s", number, suffix))
}

func (p *azureProvider) PullRequestThreads(ctx context.Context, repo RepoURL, number int) ([]PullRequestThread, error) {
	if number <= 0 {
		return nil, fmt.Errorf("pull request number must be positive")
	}
	var result struct {
		Value []azureThread `json:"value"`
	}
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.threadsEndpoint(repo, number, ""), Out: &result}); err != nil {
		return nil, err
	}
	var threads []PullRequestThread
	for _, raw := range result.Value {
		thread, ok, err := raw.read(repo, number)
		if err != nil {
			return nil, err
		}
		if ok {
			threads = append(threads, thread)
		}
	}
	return threads, nil
}

// thread decodes one thread Azure answered with, which must hold a comment someone wrote.
func (p *azureProvider) thread(ctx context.Context, repo RepoURL, number int, request restRequest) (PullRequestThread, error) {
	var raw azureThread
	request.Out = &raw
	if _, err := p.api.do(ctx, request); err != nil {
		return PullRequestThread{}, err
	}
	thread, ok, err := raw.read(repo, number)
	if err != nil {
		return thread, err
	}
	if !ok {
		return thread, fmt.Errorf("azure DevOps pull request %d thread %d holds no comment someone wrote", number, raw.ID)
	}
	return thread, nil
}

func (p *azureProvider) PullRequestThread(ctx context.Context, repo RepoURL, number int, thread string) (PullRequestThread, error) {
	id, err := azureThreadID(thread)
	if err != nil {
		return PullRequestThread{}, err
	}
	return p.thread(ctx, repo, number, restRequest{Method: http.MethodGet, Endpoint: p.threadsEndpoint(repo, number, "/"+id)})
}

func (p *azureProvider) CreatePullRequestThread(ctx context.Context, repo RepoURL, number int, request PullRequestThreadRequest) (PullRequestThread, error) {
	if err := validateComment(number, request.Body); err != nil {
		return PullRequestThread{}, err
	}
	payload := map[string]any{"status": "active",
		"comments": []map[string]any{{"content": request.Body, "parentCommentId": 0, "commentType": 1}}}
	if request.Path != "" {
		if request.StartLine <= 0 || request.EndLine < request.StartLine || request.HeadSHA == "" || request.BaseSHA == "" {
			return PullRequestThread{}, fmt.Errorf("a thread on %s needs its line range and the head and base of its diff", request.Path)
		}
		change, err := p.threadChange(ctx, repo, number, request)
		if err != nil {
			return PullRequestThread{}, err
		}
		side := "rightFile"
		if request.Old {
			side = "leftFile"
		}
		payload["threadContext"] = map[string]any{"filePath": "/" + strings.TrimPrefix(request.Path, "/"),
			side + "Start": map[string]int{"line": request.StartLine, "offset": 0}, side + "End": map[string]int{"line": request.EndLine, "offset": 0}}
		payload["pullRequestThreadContext"] = change
	}
	return p.thread(ctx, repo, number, restRequest{Method: http.MethodPost, Endpoint: p.threadsEndpoint(repo, number, ""), Body: payload})
}

// threadChange is the thread context placing request's lines on the pull request iteration whose
// diff they were read from: its head and merge base are the request's, and the head is still the
// pull request's.
func (p *azureProvider) threadChange(ctx context.Context, repo RepoURL, number int, request PullRequestThreadRequest) (map[string]any, error) {
	var pull azurePull
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.pullEndpoint(repo, fmt.Sprintf("/%d", number)), Out: &pull}); err != nil {
		return nil, err
	}
	if pull.LastMergeSourceCommit.CommitID != request.HeadSHA {
		return nil, fmt.Errorf("%w: its head is %s, not %s", ErrThreadTargetMoved, pull.LastMergeSourceCommit.CommitID, request.HeadSHA)
	}
	var iterations struct {
		Value []struct {
			ID     int `json:"id"`
			Source struct {
				ID string `json:"commitId"`
			} `json:"sourceRefCommit"`
			Common struct {
				ID string `json:"commitId"`
			} `json:"commonRefCommit"`
		} `json:"value"`
	}
	path := fmt.Sprintf("/%d/iterations", number)
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.pullEndpoint(repo, path), Out: &iterations}); err != nil {
		return nil, err
	}
	iteration := 0
	for _, candidate := range iterations.Value {
		if candidate.Source.ID == request.HeadSHA && candidate.Common.ID == request.BaseSHA && candidate.ID > iteration {
			iteration = candidate.ID
		}
	}
	if iteration == 0 {
		return nil, fmt.Errorf("%w: no iteration has head %s and base %s", ErrThreadTargetMoved, request.HeadSHA, request.BaseSHA)
	}
	want := "/" + strings.TrimPrefix(request.Path, "/")
	for skip := 0; ; {
		var changes struct {
			Entries []struct {
				ID   int `json:"changeTrackingId"`
				Item struct {
					Path string `json:"path"`
				} `json:"item"`
				OriginalPath string `json:"originalPath"`
			} `json:"changeEntries"`
			NextSkip int `json:"nextSkip"`
			NextTop  int `json:"nextTop"`
		}
		query := url.Values{"api-version": {azureAPIVersion}, "$compareTo": {"0"}, "$top": {"2000"}, "$skip": {strconv.Itoa(skip)}}
		endpoint := p.api.endpoint("/"+url.PathEscape(repo.Project)+"/_apis/git/repositories/"+url.PathEscape(repo.Name)+"/pullrequests"+
			fmt.Sprintf("%s/%d/changes", path, iteration), query)
		if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: endpoint, Out: &changes}); err != nil {
			return nil, err
		}
		for _, entry := range changes.Entries {
			changed := entry.Item.Path
			if request.Old && entry.OriginalPath != "" {
				changed = entry.OriginalPath
			}
			if changed == want && entry.ID > 0 {
				return map[string]any{"changeTrackingId": entry.ID,
					"iterationContext": map[string]int{"firstComparingIteration": 0, "secondComparingIteration": iteration}}, nil
			}
		}
		if changes.NextTop == 0 {
			return nil, fmt.Errorf("%s is not in pull request %d's diff", request.Path, number)
		}
		if changes.NextSkip <= skip {
			return nil, fmt.Errorf("azure DevOps pull request %d's changes did not page forward", number)
		}
		skip = changes.NextSkip
	}
}

func (p *azureProvider) ReplyPullRequestThread(ctx context.Context, repo RepoURL, number int, thread, parent, body string) (PullRequestThreadComment, error) {
	if err := validateComment(number, body); err != nil {
		return PullRequestThreadComment{}, err
	}
	id, err := azureThreadID(thread)
	if err != nil {
		return PullRequestThreadComment{}, err
	}
	parentID, err := strconv.ParseInt(parent, 10, 64)
	if err != nil || parentID <= 0 {
		return PullRequestThreadComment{}, fmt.Errorf("azure DevOps comment ID %q is not a positive number", parent)
	}
	var reply azureThreadComment
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodPost, Endpoint: p.threadsEndpoint(repo, number, "/"+id+"/comments"),
		Body: map[string]any{"content": body, "parentCommentId": parentID, "commentType": 1}, Out: &reply}); err != nil {
		return PullRequestThreadComment{}, err
	}
	if reply.ID <= 0 {
		return PullRequestThreadComment{}, fmt.Errorf("azure DevOps answered pull request %d thread %s's reply with no comment ID", number, id)
	}
	return reply.read(), nil
}

func (p *azureProvider) ResolvePullRequestThread(ctx context.Context, repo RepoURL, number int, thread string, resolved bool) (PullRequestThread, error) {
	id, err := azureThreadID(thread)
	if err != nil {
		return PullRequestThread{}, err
	}
	status, verb := "active", "reopening"
	if resolved {
		status, verb = "fixed", "resolving"
	}
	changed, err := p.thread(ctx, repo, number, restRequest{Method: http.MethodPatch, Endpoint: p.threadsEndpoint(repo, number, "/"+id),
		Body: map[string]string{"status": status}})
	if err != nil {
		return changed, err
	}
	if changed.Resolved != resolved {
		return changed, fmt.Errorf("azure DevOps left pull request %d thread %s %s instead of %s it", number, id, changed.Status, verb)
	}
	return changed, nil
}

func azureThreadID(thread string) (string, error) {
	if id, err := strconv.Atoi(thread); err != nil || id <= 0 {
		return "", fmt.Errorf("azure DevOps thread ID %q is not a positive number", thread)
	}
	return thread, nil
}
