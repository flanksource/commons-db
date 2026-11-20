// The CI a pull request ran: the current execution of each pipeline that built its merge result
// or head, with the jobs and steps of its latest attempt, and their logs.
package git

import (
	"context"
	"time"
)

// PullRequestExecution is the current execution of one pipeline for a pull request. Repository
// is the repository whose pipeline ran it: a fork's, for an execution of the fork's branch.
type PullRequestExecution struct {
	PipelineExecution
	PipelineName string        `json:"pipelineName"`
	Repository   RepoURL       `json:"repository"`
	Jobs         []PipelineJob `json:"jobs,omitempty"`
}

// PipelineJob is one job of an execution's latest attempt. Status and Result are the provider's
// own; LogID names its log for PipelineLog, empty when it has none.
type PipelineJob struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	Result     string         `json:"result,omitempty"`
	StartedAt  time.Time      `json:"startedAt"`
	FinishedAt time.Time      `json:"finishedAt"`
	URL        string         `json:"url,omitempty"`
	LogID      string         `json:"logId,omitempty"`
	Steps      []PipelineStep `json:"steps,omitempty"`
}

// PipelineStep is one step of a job, numbered in the order it runs.
type PipelineStep struct {
	Name   string `json:"name"`
	Number int    `json:"number"`
	Status string `json:"status"`
	Result string `json:"result,omitempty"`
	LogID  string `json:"logId,omitempty"`
}

// PullRequestPipelineReader reads the CI a pull request ran. PipelineLog is the last tail lines of
// an execution's log, all of it when tail is 0, read through the execution's Repository.
type PullRequestPipelineReader interface {
	PullRequestExecutions(ctx context.Context, repo RepoURL, number int) ([]PullRequestExecution, error)
	PipelineLog(ctx context.Context, repo RepoURL, execution, log string, tail int) (string, error)
}
