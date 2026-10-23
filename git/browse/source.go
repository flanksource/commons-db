package browse

import (
	"context"
	"fmt"
	"sort"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
)

func New(options Options) (*Service, error) {
	if options.Catalog == nil || options.Resolve == nil || options.CacheDir == "" {
		return nil, fmt.Errorf("new git browser: catalog, connection resolver and cache directory are required")
	}
	return &Service{options: options, cache: &git.Cache{Root: options.CacheDir}}, nil
}

func IsGitType(kind string) bool {
	switch kind {
	case models.ConnectionTypeGit, models.ConnectionTypeGithub, models.ConnectionTypeGitlab, models.ConnectionTypeAzureDevops:
		return true
	}
	return false
}

func (s *Service) Connections(ctx context.Context) ([]Connection, error) {
	catalog, err := s.options.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	connections := []Connection{}
	for _, connection := range catalog {
		if IsGitType(connection.Type) {
			connections = append(connections, Connection{Name: connection.Name, Type: connection.Type, URL: git.Redact(connection.URL)})
		}
	}
	sort.Slice(connections, func(i, j int) bool { return connections[i].Name < connections[j].Name })
	return connections, nil
}

func (s *Service) client(ctx context.Context, name string) (*git.Client, models.Connection, error) {
	if strings.TrimSpace(name) == "" {
		return nil, models.Connection{}, fmt.Errorf("%w: connection query parameter is required", ErrInvalid)
	}
	connection, err := s.options.Resolve(ctx, name)
	if err != nil {
		return nil, models.Connection{}, err
	}
	if !IsGitType(connection.Type) {
		return nil, connection, fmt.Errorf("%w: connection %q has type %q, want a Git connection", ErrInvalid, name, connection.Type)
	}
	client, err := git.FromConnection(dbcontext.NewContext(ctx), connection)
	return client, connection, err
}

func (s *Service) open(ctx context.Context, request Request) (*git.Client, *git.Repository, error) {
	client, connection, err := s.client(ctx, request.Connection)
	if err != nil {
		return nil, nil, err
	}
	cloneURL := connection.URL
	if client.Type == models.ConnectionTypeGit {
		if request.Repo != "" {
			return nil, nil, fmt.Errorf("%w: git connection %q pins one repository", ErrInvalid, request.Connection)
		}
	} else {
		if request.Repo == "" {
			return nil, nil, fmt.Errorf("%w: name the repository for connection %q", ErrInvalid, request.Connection)
		}
		repos, err := client.Provider.ListRepositories(ctx, git.ListRepositoriesOptions{})
		if err != nil {
			return nil, nil, err
		}
		matched, err := findRepository(request.Repo, repos)
		if err != nil {
			return nil, nil, err
		}
		cloneURL = matched.CloneURL
	}
	repo, err := client.Repository(cloneURL)
	return client, repo, err
}

func findRepository(name string, repos []git.RepositoryInfo) (git.RepositoryInfo, error) {
	var matches []git.RepositoryInfo
	for _, repo := range repos {
		if strings.EqualFold(repo.FullName, name) {
			return repo, nil
		}
		if strings.EqualFold(repo.Name, name) {
			matches = append(matches, repo)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return git.RepositoryInfo{}, fmt.Errorf("%w: repository %q matches %d repositories; use its full name", ErrInvalid, name, len(matches))
}

func resolve(ctx context.Context, repo *git.Repository, ref string) (git.Ref, error) {
	if strings.TrimSpace(ref) == "" {
		return git.Ref{}, fmt.Errorf("%w: ref is required", ErrInvalid)
	}
	refs, err := repo.LsRemote(ctx)
	if err != nil {
		return git.Ref{}, err
	}
	resolved, err := refs.Resolve(ref)
	if err != nil {
		return git.Ref{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return resolved, nil
}
