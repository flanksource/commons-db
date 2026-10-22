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

func githubPipelineQuery(options PipelineOptions) (url.Values, error) {
	page := 1
	if options.Cursor != "" {
		var err error
		page, err = strconv.Atoi(options.Cursor)
		if err != nil || page < 1 {
			return nil, fmt.Errorf("invalid GitHub pagination cursor")
		}
	}
	return url.Values{"per_page": {strconv.Itoa(options.Limit)}, "page": {strconv.Itoa(page)}}, nil
}

func githubNextCursor(headers http.Header) string {
	match := linkNextPattern.FindStringSubmatch(headers.Get("Link"))
	if match == nil {
		return ""
	}
	next, err := url.Parse(match[1])
	if err != nil {
		return ""
	}
	return next.Query().Get("page")
}

func (p *githubProvider) ListPipelines(ctx context.Context, options PipelineOptions) (Page[Pipeline], error) {
	options, err := options.normalize(false)
	if err != nil {
		return Page[Pipeline]{}, err
	}
	query, err := githubPipelineQuery(options)
	if err != nil {
		return Page[Pipeline]{}, err
	}
	var response struct {
		Workflows []struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Path    string `json:"path"`
			HTMLURL string `json:"html_url"`
			State   string `json:"state"`
		} `json:"workflows"`
	}
	headers, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.repoEndpoint(options.Repository, "/actions/workflows") + "?" + query.Encode(), Out: &response})
	if err != nil {
		return Page[Pipeline]{}, err
	}
	page := Page[Pipeline]{Items: make([]Pipeline, 0, len(response.Workflows)), NextCursor: githubNextCursor(headers)}
	for _, workflow := range response.Workflows {
		if workflow.ID <= 0 || workflow.Name == "" {
			return Page[Pipeline]{}, fmt.Errorf("GitHub workflow is missing ID or name")
		}
		page.Items = append(page.Items, Pipeline{ID: strconv.FormatInt(workflow.ID, 10), Name: workflow.Name, Path: workflow.Path, URL: workflow.HTMLURL, State: workflow.State})
	}
	return page, nil
}

type githubExecution struct {
	ID           int64     `json:"id"`
	WorkflowID   int64     `json:"workflow_id"`
	RunNumber    int64     `json:"run_number"`
	RunAttempt   int       `json:"run_attempt"`
	DisplayTitle string    `json:"display_title"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	HeadBranch   string    `json:"head_branch"`
	HeadSHA      string    `json:"head_sha"`
	CreatedAt    time.Time `json:"created_at"`
	HTMLURL      string    `json:"html_url"`
	Actor        struct {
		Login string `json:"login"`
	} `json:"actor"`
}

func (p *githubProvider) ListPipelineExecutions(ctx context.Context, options PipelineOptions) (Page[PipelineExecution], error) {
	options, err := options.normalize(true)
	if err != nil {
		return Page[PipelineExecution]{}, err
	}
	query, err := githubPipelineQuery(options)
	if err != nil {
		return Page[PipelineExecution]{}, err
	}
	if options.Branch != "" {
		query.Set("branch", strings.TrimPrefix(options.Branch, "refs/heads/"))
	}
	var response struct {
		Runs []githubExecution `json:"workflow_runs"`
	}
	headers, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.repoEndpoint(options.Repository, "/actions/workflows/"+options.Pipeline+"/runs") + "?" + query.Encode(), Out: &response})
	if err != nil {
		return Page[PipelineExecution]{}, err
	}
	page := Page[PipelineExecution]{Items: make([]PipelineExecution, 0, len(response.Runs)), NextCursor: githubNextCursor(headers)}
	for _, run := range response.Runs {
		if run.ID <= 0 || strconv.FormatInt(run.WorkflowID, 10) != options.Pipeline || !fullSHAPattern.MatchString(run.HeadSHA) {
			return Page[PipelineExecution]{}, fmt.Errorf("GitHub run %d has invalid workflow or source SHA", run.ID)
		}
		branch := run.HeadBranch
		if branch != "" && !strings.HasPrefix(branch, "refs/") {
			branch = "refs/heads/" + branch
		}
		page.Items = append(page.Items, PipelineExecution{ID: strconv.FormatInt(run.ID, 10), Pipeline: options.Pipeline,
			Number: strconv.FormatInt(run.RunNumber, 10), Name: run.DisplayTitle, Attempt: run.RunAttempt, Branch: branch,
			SHA: run.HeadSHA, Status: run.Status, Result: run.Conclusion, Author: run.Actor.Login, CreatedAt: run.CreatedAt, URL: run.HTMLURL})
	}
	return page, nil
}
