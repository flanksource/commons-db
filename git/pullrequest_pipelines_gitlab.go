// GitLab merge request CI: the merge request's latest pipeline, run in its source project, with
// that pipeline's jobs and each job's trace as its log.
package git

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

type gitlabPipeline struct {
	ID        int64     `json:"id"`
	ProjectID int64     `json:"project_id"`
	SHA       string    `json:"sha"`
	Ref       string    `json:"ref"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	WebURL    string    `json:"web_url"`
}

type gitlabJob struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	WebURL     string    `json:"web_url"`
}

// PullRequestExecutions is the merge request's latest pipeline, which GitLab lists first, with
// its jobs in the order they were created. A fork's pipeline runs in the fork's project, which
// its Repository names.
func (p *gitlabProvider) PullRequestExecutions(ctx context.Context, repo RepoURL, number int) ([]PullRequestExecution, error) {
	pull, err := p.GetPullRequest(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	var pipelines []gitlabPipeline
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.mrEndpoint(repo, fmt.Sprintf("/%d/pipelines", number)), Out: &pipelines}); err != nil {
		return nil, err
	}
	if len(pipelines) == 0 {
		return nil, nil
	}
	latest := pipelines[0]
	var jobs []gitlabJob
	endpoint := p.api.endpoint(fmt.Sprintf("/api/v4/projects/%d/pipelines/%d/jobs", latest.ProjectID, latest.ID), nil)
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: endpoint + "?per_page=100", Out: &jobs}); err != nil {
		return nil, fmt.Errorf("gitlab pipeline %d jobs: %w", latest.ID, err)
	}
	slices.SortFunc(jobs, func(a, b gitlabJob) int { return cmp.Compare(a.ID, b.ID) })
	id := strconv.FormatInt(latest.ID, 10)
	execution := PullRequestExecution{PipelineName: "pipeline", Repository: repo, PipelineExecution: PipelineExecution{
		ID: id, Number: id, Branch: latest.Ref, SHA: latest.SHA, Status: latest.Status, CreatedAt: latest.CreatedAt, URL: latest.WebURL}}
	if owner, name, found := cutLast(pull.HeadRepository, "/"); found && pull.HeadRepository != repo.Owner+"/"+repo.Name {
		execution.Repository.Owner, execution.Repository.Name = owner, name
	}
	for _, job := range jobs {
		jobID := strconv.FormatInt(job.ID, 10)
		execution.Jobs = append(execution.Jobs, PipelineJob{ID: jobID, Name: job.Name, Status: job.Status, StartedAt: job.StartedAt,
			FinishedAt: job.FinishedAt, URL: job.WebURL, LogID: jobID})
	}
	return []PullRequestExecution{execution}, nil
}

// PipelineLog is the last tail lines of job log's trace, all of it when tail is 0, read through
// repo, the project the job ran in.
func (p *gitlabProvider) PipelineLog(ctx context.Context, repo RepoURL, _, log string, tail int) (string, error) {
	if tail < 0 {
		return "", fmt.Errorf("log tail must not be negative")
	}
	job, err := strconv.ParseInt(log, 10, 64)
	if err != nil || job <= 0 {
		return "", fmt.Errorf("gitlab job ID %q is not a positive number", log)
	}
	var body strings.Builder
	if _, err := p.api.do(ctx, restRequest{Method: http.MethodGet, Endpoint: p.projectEndpoint(repo, fmt.Sprintf("/jobs/%d/trace", job)), Out: &body}); err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(body.String(), "\r\n", "\n"), "\n"), "\n")
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return strings.Join(lines, "\n"), nil
}

// cutLast cuts s around the last instance of sep.
func cutLast(s, sep string) (string, string, bool) {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}
