package browse

import (
	"context"

	"github.com/flanksource/commons-db/git"
)

func (s *Service) Repositories(ctx context.Context, request Request) ([]Repository, error) {
	client, _, err := s.client(ctx, request.Connection)
	if err != nil {
		return nil, err
	}
	repos, err := client.Provider.ListRepositories(ctx, git.ListRepositoriesOptions{})
	if err != nil {
		return nil, err
	}
	output := make([]Repository, 0, len(repos))
	for _, repo := range repos {
		output = append(output, Repository{Name: repo.Name, FullName: repo.FullName, Namespace: repo.Namespace,
			CloneURL: git.Redact(repo.CloneURL), WebURL: repo.WebURL, DefaultBranch: repo.DefaultBranch, Private: repo.Private, Archived: repo.Archived})
	}
	return output, nil
}

func (s *Service) Refs(ctx context.Context, request Request) (Refs, error) {
	_, repo, err := s.open(ctx, request)
	if err != nil {
		return Refs{}, err
	}
	remote, err := repo.LsRemote(ctx)
	if err != nil {
		return Refs{}, err
	}
	output := Refs{Branches: make([]Ref, 0, len(remote.Branches)), Tags: make([]Ref, 0, len(remote.Tags))}
	for _, ref := range remote.Branches {
		output.Branches = append(output.Branches, Ref{Name: ref.Name, SHA: ref.SHA})
	}
	for _, ref := range remote.Tags {
		output.Tags = append(output.Tags, Ref{Name: ref.Name, SHA: ref.SHA})
	}
	if remote.Head != nil {
		output.Head = &Ref{Name: remote.Head.Name, SHA: remote.Head.SHA}
	}
	return output, nil
}

func (s *Service) Pipelines(ctx context.Context, request Request) (git.Page[git.Pipeline], error) {
	client, repo, err := s.open(ctx, request)
	if err != nil {
		return git.Page[git.Pipeline]{}, err
	}
	provider, err := git.PipelineAPI(client.Provider)
	if err != nil {
		return git.Page[git.Pipeline]{}, err
	}
	return provider.ListPipelines(ctx, git.PipelineOptions{Repository: repo.URL, Cursor: request.Cursor, Limit: request.Limit})
}

func (s *Service) Executions(ctx context.Context, request Request) (git.Page[git.PipelineExecution], error) {
	client, repo, err := s.open(ctx, request)
	if err != nil {
		return git.Page[git.PipelineExecution]{}, err
	}
	provider, err := git.PipelineAPI(client.Provider)
	if err != nil {
		return git.Page[git.PipelineExecution]{}, err
	}
	return provider.ListPipelineExecutions(ctx, git.PipelineOptions{Repository: repo.URL, Pipeline: request.Pipeline, Branch: request.Branch, Cursor: request.Cursor, Limit: request.Limit})
}
