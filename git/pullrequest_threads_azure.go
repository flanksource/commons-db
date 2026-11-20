// Azure DevOps pull request threads: each live thread's text comments, its status read as
// resolved or not, and the file and line its context places it on.
package git

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type azureThread struct {
	ID      int             `json:"id"`
	Status  json.RawMessage `json:"status"`
	Deleted bool            `json:"isDeleted"`
	Context *struct {
		Path  string `json:"filePath"`
		Right *struct {
			Line int `json:"line"`
		} `json:"rightFileStart"`
	} `json:"threadContext"`
	Comments []struct {
		ID      int64     `json:"id"`
		Body    string    `json:"content"`
		Kind    string    `json:"commentType"`
		Deleted bool      `json:"isDeleted"`
		Created time.Time `json:"publishedDate"`
		Author  struct {
			Name   string `json:"displayName"`
			Login  string `json:"uniqueName"`
			Avatar string `json:"imageUrl"`
		} `json:"author"`
	} `json:"comments"`
}

// azureThreadResolved reads a thread status, which Azure sends as a name or its number.
func azureThreadResolved(status json.RawMessage) (bool, bool) {
	switch string(status) {
	case `1`, `6`, `"active"`, `"pending"`:
		return false, true
	case `2`, `3`, `4`, `5`, `"fixed"`, `"wontFix"`, `"closed"`, `"byDesign"`:
		return true, true
	}
	return false, false
}

func (p *azureProvider) PullRequestThreads(ctx context.Context, repo RepoURL, number int) ([]PullRequestThread, error) {
	if number <= 0 {
		return nil, fmt.Errorf("pull request number must be positive")
	}
	var result struct {
		Value []azureThread `json:"value"`
	}
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.pullEndpoint(repo, fmt.Sprintf("/%d/threads", number)), Out: &result}); err != nil {
		return nil, err
	}
	var threads []PullRequestThread
	for _, thread := range result.Value {
		if thread.Deleted {
			continue
		}
		read := PullRequestThread{ID: strconv.Itoa(thread.ID), URL: fmt.Sprintf("%s/pullrequest/%d?discussionId=%d", repo.azureWebBase(), number, thread.ID)}
		for _, comment := range thread.Comments {
			if comment.Deleted || comment.Kind != "text" || comment.Body == "" {
				continue
			}
			read.Comments = append(read.Comments, PullRequestThreadComment{ID: strconv.FormatInt(comment.ID, 10), Author: comment.Author.Name,
				AuthorLogin: comment.Author.Login, AuthorAvatarURL: comment.Author.Avatar, Body: comment.Body, CreatedAt: comment.Created})
		}
		if len(read.Comments) == 0 {
			continue
		}
		resolved, known := azureThreadResolved(thread.Status)
		if !known {
			return nil, fmt.Errorf("azure DevOps pull request %d thread %d has unknown status %s", number, thread.ID, thread.Status)
		}
		read.Resolved = resolved
		if thread.Context != nil {
			read.Path = thread.Context.Path
			if thread.Context.Right != nil {
				read.Line = thread.Context.Right.Line
			}
		}
		threads = append(threads, read)
	}
	return threads, nil
}
