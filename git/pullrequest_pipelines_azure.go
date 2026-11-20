// Azure DevOps pull request CI: the latest build of each pipeline that built the pull request's
// merge result or source branch at its current commit, its timeline's jobs and tasks, and logs.
package git

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type azureBuild struct {
	azureExecution
	Definition struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"definition"`
}

type azureTimelineRecord struct {
	ID         string    `json:"id"`
	ParentID   string    `json:"parentId"`
	Identifier string    `json:"identifier"`
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	State      string    `json:"state"`
	Result     string    `json:"result"`
	Order      int       `json:"order"`
	Attempt    int       `json:"attempt"`
	Started    time.Time `json:"startTime"`
	Finished   time.Time `json:"finishTime"`
	Log        *struct {
		ID int64 `json:"id"`
	} `json:"log"`
}

func (r azureTimelineRecord) logID() string {
	if r.Log == nil || r.Log.ID <= 0 {
		return ""
	}
	return strconv.FormatInt(r.Log.ID, 10)
}

// PullRequestExecutions reads the latest build of each pipeline that built the pull request's
// merge result (refs/pull/N/merge at its last merge commit) or its source branch at its head,
// the source branch's in the fork's project for a fork.
func (p *azureProvider) PullRequestExecutions(ctx context.Context, repo RepoURL, number int) ([]PullRequestExecution, error) {
	if number <= 0 {
		return nil, fmt.Errorf("pull request number must be positive")
	}
	var pull azurePull
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.pullEndpoint(repo, fmt.Sprintf("/%d", number)), Out: &pull}); err != nil {
		return nil, err
	}
	source := pull.Repository
	if pull.ForkSource != nil {
		source = pull.ForkSource.Repository
	}
	if pull.Repository.ID == "" || source.ID == "" || source.Project.Name == "" {
		return nil, fmt.Errorf("azure DevOps pull request %d has no repository identity for its builds", number)
	}
	fork := repo
	fork.Project, fork.Name = source.Project.Name, source.Name
	queries := []struct {
		repository RepoURL
		id, branch string
		sha        string
	}{
		{repo, pull.Repository.ID, fmt.Sprintf("refs/pull/%d/merge", number), pull.LastMergeCommit.CommitID},
		{fork, source.ID, pull.SourceRefName, pull.LastMergeSourceCommit.CommitID},
	}
	type current struct {
		build      azureBuild
		repository RepoURL
	}
	latest := map[int64]current{}
	for _, query := range queries {
		if query.sha == "" {
			continue
		}
		builds, err := p.pullRequestBuilds(ctx, query.repository.Project, query.id, query.branch)
		if err != nil {
			return nil, err
		}
		for _, build := range builds {
			if build.SourceBranch != query.branch || build.SourceVersion != query.sha {
				continue
			}
			if build.ID <= 0 || build.Definition.ID <= 0 || build.Definition.Name == "" {
				return nil, fmt.Errorf("azure build %d has no identity or pipeline definition", build.ID)
			}
			old, found := latest[build.Definition.ID]
			if !found || build.QueueTime.After(old.build.QueueTime) || (build.QueueTime.Equal(old.build.QueueTime) && build.ID > old.build.ID) {
				latest[build.Definition.ID] = current{build: build, repository: query.repository}
			}
		}
	}
	definitions := make([]int64, 0, len(latest))
	for definition := range latest {
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i] < definitions[j] })
	executions := make([]PullRequestExecution, 0, len(definitions))
	for _, definition := range definitions {
		build, repository := latest[definition].build, latest[definition].repository
		execution := PullRequestExecution{PipelineName: build.Definition.Name, Repository: repository, PipelineExecution: PipelineExecution{
			ID: strconv.FormatInt(build.ID, 10), Pipeline: strconv.FormatInt(definition, 10), Number: build.BuildNumber, Name: build.BuildNumber,
			Branch: build.SourceBranch, SHA: build.SourceVersion, Status: build.Status, Result: build.Result,
			Author: build.RequestedFor.DisplayName, CreatedAt: build.QueueTime, URL: build.Links.Web.Href}}
		if build.Status != "notStarted" && build.Status != "postponed" {
			jobs, err := p.buildJobs(ctx, repository.Project, build.ID, execution.URL)
			if err != nil {
				return nil, err
			}
			execution.Jobs = jobs
		}
		executions = append(executions, execution)
	}
	return executions, nil
}

func (p *azureProvider) pullRequestBuilds(ctx context.Context, project, repositoryID, branch string) ([]azureBuild, error) {
	query := url.Values{"api-version": {azureAPIVersion}, "repositoryId": {repositoryID}, "repositoryType": {"TfsGit"},
		"branchName": {branch}, "queryOrder": {"queueTimeDescending"}}
	var response struct {
		Value []azureBuild `json:"value"`
	}
	path := "/" + url.PathEscape(project) + "/_apis/build/builds"
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.api.endpoint(path, query), Out: &response}); err != nil {
		return nil, err
	}
	return response.Value, nil
}

// buildJobs are the jobs of a build's timeline, each the latest attempt of its job, with the tasks
// under it as steps.
func (p *azureProvider) buildJobs(ctx context.Context, project string, build int64, buildURL string) ([]PipelineJob, error) {
	var timeline struct {
		Records *[]azureTimelineRecord `json:"records"`
	}
	path := fmt.Sprintf("/%s/_apis/build/builds/%d/timeline", url.PathEscape(project), build)
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.endpoint(path), Out: &timeline}); err != nil {
		return nil, err
	}
	if timeline.Records == nil {
		return nil, fmt.Errorf("azure build %d timeline has no records", build)
	}
	records := latestAttempts(*timeline.Records)
	byID := make(map[string]azureTimelineRecord, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	var jobs []PipelineJob
	for _, record := range records {
		if record.Type != "Job" {
			continue
		}
		job := PipelineJob{ID: record.ID, Name: record.Name, Status: record.State, Result: record.Result, StartedAt: record.Started,
			FinishedAt: record.Finished, URL: buildURL + "&view=logs&j=" + url.QueryEscape(record.ID), LogID: record.logID()}
		for _, task := range records {
			if task.Type == "Task" && underJob(task, record.ID, byID) {
				job.Steps = append(job.Steps, PipelineStep{Name: task.Name, Number: task.Order, Status: task.State, Result: task.Result, LogID: task.logID()})
			}
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// latestAttempts keeps the latest attempt of each timeline record a retry repeated, in order.
func latestAttempts(records []azureTimelineRecord) []azureTimelineRecord {
	latest := map[string]azureTimelineRecord{}
	for _, record := range records {
		key := record.ID
		if record.Identifier != "" {
			key = record.Type + ":" + record.ParentID + ":" + record.Identifier
		}
		if old, ok := latest[key]; !ok || record.Attempt >= old.Attempt {
			latest[key] = record
		}
	}
	out := make([]azureTimelineRecord, 0, len(latest))
	for _, record := range latest {
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// underJob reports whether task sits under the job, directly or through a phase.
func underJob(task azureTimelineRecord, job string, records map[string]azureTimelineRecord) bool {
	seen := map[string]bool{}
	for id := task.ParentID; id != "" && !seen[id]; id = records[id].ParentID {
		if id == job {
			return true
		}
		seen[id] = true
	}
	return false
}

// PipelineLog reads the last tail lines of a build's log, all of it when tail is 0, fetching only
// those lines.
func (p *azureProvider) PipelineLog(ctx context.Context, repo RepoURL, execution, log string, tail int) (string, error) {
	if tail < 0 {
		return "", fmt.Errorf("log tail must not be negative")
	}
	build, err := strconv.ParseInt(execution, 10, 64)
	if err != nil || build <= 0 {
		return "", fmt.Errorf("azure build ID %q is not a positive number", execution)
	}
	path := fmt.Sprintf("/%s/_apis/build/builds/%d/logs", url.PathEscape(repo.Project), build)
	var logs struct {
		Value []struct {
			ID    int64 `json:"id"`
			Lines int64 `json:"lineCount"`
		} `json:"value"`
	}
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.endpoint(path), Out: &logs}); err != nil {
		return "", err
	}
	for _, found := range logs.Value {
		if strconv.FormatInt(found.ID, 10) != log {
			continue
		}
		start := int64(0)
		if tail > 0 {
			start = max(0, found.Lines-int64(tail))
		}
		query := url.Values{"api-version": {azureAPIVersion}, "startLine": {strconv.FormatInt(start, 10)}, "endLine": {strconv.FormatInt(found.Lines, 10)}}
		var body strings.Builder
		if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.api.endpoint(path+"/"+log, query), Out: &body}); err != nil {
			return "", err
		}
		lines := strings.Split(strings.TrimRight(strings.ReplaceAll(body.String(), "\r\n", "\n"), "\n"), "\n")
		if tail > 0 && len(lines) > tail {
			lines = lines[len(lines)-tail:]
		}
		return strings.Join(lines, "\n"), nil
	}
	return "", fmt.Errorf("azure build %d has no log %s", build, log)
}
