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

type azureWebLinks struct {
	Web struct {
		Href string `json:"href"`
	} `json:"web"`
}

func (p *azureProvider) pipelineQuery(ctx context.Context, options PipelineOptions) (url.Values, error) {
	if options.Repository.Project == "" {
		return nil, fmt.Errorf("Azure pipeline repository requires a project")
	}
	var repo struct {
		ID string `json:"id"`
	}
	path := "/" + url.PathEscape(options.Repository.Project) + "/_apis/git/repositories/" + url.PathEscape(options.Repository.Name)
	if _, err := p.api.do(ctx, http.MethodGet, p.endpoint(path), nil, &repo); err != nil {
		return nil, err
	}
	if repo.ID == "" {
		return nil, fmt.Errorf("Azure repository %q has no ID", options.Repository.Name)
	}
	query := url.Values{"api-version": {azureAPIVersion}, "repositoryId": {repo.ID}, "repositoryType": {"TfsGit"}, "$top": {strconv.Itoa(options.Limit)}}
	if options.Cursor != "" {
		query.Set("continuationToken", options.Cursor)
	}
	return query, nil
}

func (p *azureProvider) ListPipelines(ctx context.Context, options PipelineOptions) (Page[Pipeline], error) {
	options, err := options.normalize(false)
	if err != nil {
		return Page[Pipeline]{}, err
	}
	query, err := p.pipelineQuery(ctx, options)
	if err != nil {
		return Page[Pipeline]{}, err
	}
	var response struct {
		Value []struct {
			ID    int64         `json:"id"`
			Name  string        `json:"name"`
			Path  string        `json:"path"`
			Links azureWebLinks `json:"_links"`
		} `json:"value"`
	}
	path := "/" + url.PathEscape(options.Repository.Project) + "/_apis/build/definitions"
	headers, err := p.api.do(ctx, http.MethodGet, p.api.endpoint(path, query), nil, &response)
	if err != nil {
		return Page[Pipeline]{}, err
	}
	page := Page[Pipeline]{Items: make([]Pipeline, 0, len(response.Value)), NextCursor: headers.Get("x-ms-continuationtoken")}
	for _, definition := range response.Value {
		if definition.ID <= 0 || definition.Name == "" {
			return Page[Pipeline]{}, fmt.Errorf("Azure pipeline is missing ID or name")
		}
		page.Items = append(page.Items, Pipeline{ID: strconv.FormatInt(definition.ID, 10), Name: definition.Name, Path: definition.Path, URL: definition.Links.Web.Href})
	}
	return page, nil
}

type azureExecution struct {
	ID            int64         `json:"id"`
	BuildNumber   string        `json:"buildNumber"`
	Status        string        `json:"status"`
	Result        string        `json:"result"`
	SourceBranch  string        `json:"sourceBranch"`
	SourceVersion string        `json:"sourceVersion"`
	QueueTime     time.Time     `json:"queueTime"`
	Links         azureWebLinks `json:"_links"`
	Definition    struct {
		ID int64 `json:"id"`
	} `json:"definition"`
	RequestedFor struct {
		DisplayName string `json:"displayName"`
	} `json:"requestedFor"`
}

func (p *azureProvider) ListPipelineExecutions(ctx context.Context, options PipelineOptions) (Page[PipelineExecution], error) {
	options, err := options.normalize(true)
	if err != nil {
		return Page[PipelineExecution]{}, err
	}
	query, err := p.pipelineQuery(ctx, options)
	if err != nil {
		return Page[PipelineExecution]{}, err
	}
	query.Set("definitions", options.Pipeline)
	query.Set("queryOrder", "queueTimeDescending")
	if options.Branch != "" {
		branch := options.Branch
		if !strings.HasPrefix(branch, "refs/") {
			branch = "refs/heads/" + branch
		}
		query.Set("branchName", branch)
	}
	var response struct {
		Value []azureExecution `json:"value"`
	}
	path := "/" + url.PathEscape(options.Repository.Project) + "/_apis/build/builds"
	headers, err := p.api.do(ctx, http.MethodGet, p.api.endpoint(path, query), nil, &response)
	if err != nil {
		return Page[PipelineExecution]{}, err
	}
	page := Page[PipelineExecution]{Items: make([]PipelineExecution, 0, len(response.Value)), NextCursor: headers.Get("x-ms-continuationtoken")}
	for _, run := range response.Value {
		if run.ID <= 0 || strconv.FormatInt(run.Definition.ID, 10) != options.Pipeline || !fullSHAPattern.MatchString(run.SourceVersion) {
			return Page[PipelineExecution]{}, fmt.Errorf("Azure build %d has invalid definition or source SHA", run.ID)
		}
		page.Items = append(page.Items, PipelineExecution{ID: strconv.FormatInt(run.ID, 10), Pipeline: options.Pipeline,
			Number: run.BuildNumber, Name: run.BuildNumber, Branch: run.SourceBranch, SHA: run.SourceVersion,
			Status: run.Status, Result: run.Result, Author: run.RequestedFor.DisplayName, CreatedAt: run.QueueTime, URL: run.Links.Web.Href})
	}
	return page, nil
}
