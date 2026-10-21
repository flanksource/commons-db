package git

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

var ErrPipelinesUnsupported = errors.New("pipelines are not supported by this git provider")

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

type Pipeline struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path,omitempty"`
	URL   string `json:"url,omitempty"`
	State string `json:"state,omitempty"`
}

type PipelineExecution struct {
	ID        string    `json:"id"`
	Pipeline  string    `json:"pipeline"`
	Number    string    `json:"number"`
	Name      string    `json:"name"`
	Attempt   int       `json:"attempt,omitempty"`
	Branch    string    `json:"branch"`
	SHA       string    `json:"sha"`
	Status    string    `json:"status"`
	Result    string    `json:"result,omitempty"`
	Author    string    `json:"author,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	URL       string    `json:"url,omitempty"`
}

type PipelineOptions struct {
	Repository RepoURL
	Pipeline   string
	Branch     string
	Cursor     string
	Limit      int
}

type PipelineProvider interface {
	ListPipelines(context.Context, PipelineOptions) (Page[Pipeline], error)
	ListPipelineExecutions(context.Context, PipelineOptions) (Page[PipelineExecution], error)
}

func PipelineAPI(provider Provider) (PipelineProvider, error) {
	api, ok := provider.(PipelineProvider)
	if !ok {
		return nil, ErrPipelinesUnsupported
	}
	return api, nil
}

func (o PipelineOptions) normalize(executions bool) (PipelineOptions, error) {
	if o.Limit < 0 || o.Limit > 100 {
		return o, fmt.Errorf("pipeline limit must be between 1 and 100")
	}
	if o.Limit == 0 {
		o.Limit = 50
	}
	if executions {
		id, err := strconv.ParseInt(o.Pipeline, 10, 64)
		if err != nil || id <= 0 {
			return o, fmt.Errorf("a positive pipeline ID is required")
		}
	}
	if o.Repository.Name == "" || o.Repository.Owner == "" {
		return o, fmt.Errorf("pipeline repository is required")
	}
	return o, nil
}
