package browse

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
)

func New(options Options) (*Service, error) {
	if (options.Resolve == nil && options.Client == nil) || options.CacheDir == "" {
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
	if s.options.Catalog == nil {
		return nil, fmt.Errorf("git browser requires a catalog")
	}
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
	if s.options.ClientScope == nil {
		return s.loadClient(ctx, name)
	}
	scope, err := s.options.ClientScope(ctx)
	if err != nil {
		return nil, models.Connection{}, err
	}
	entry, err := s.clients.get(ctx, memoLoad[openedClient]{key: scope + "\x00" + name, metric: "connections", owner: ctx, load: func() (openedClient, error) {
		client, connection, err := s.loadClient(ctx, name)
		return openedClient{Client: client, Connection: connection}, err
	}})
	return entry.Client, entry.Connection, err
}

func (s *Service) loadClient(ctx context.Context, name string) (*git.Client, models.Connection, error) {
	if strings.TrimSpace(name) == "" {
		return nil, models.Connection{}, fmt.Errorf("%w: connection query parameter is required", ErrInvalid)
	}
	if s.options.Client != nil {
		return s.options.Client(ctx, name)
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
	opened, err := s.Open(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	return opened.Client, opened.Repository, nil
}

func (s *Service) Open(ctx context.Context, request Request) (*Opened, error) {
	client, connection, err := s.client(ctx, request.Connection)
	if err != nil {
		return nil, err
	}
	cloneURL := connection.URL
	name := ""
	var info git.RepositoryInfo
	if client.Type == models.ConnectionTypeGit {
		if request.Repo != "" {
			return nil, fmt.Errorf("%w: git connection %q pins one repository", ErrInvalid, request.Connection)
		}
	} else {
		if request.Repo == "" {
			return nil, fmt.Errorf("%w: name the repository for connection %q", ErrInvalid, request.Connection)
		}
		repos, err := s.repositories(ctx, request.Connection, client)
		if err != nil {
			return nil, err
		}
		matched, err := FindRepository(RepositoryLookup{Name: request.Repo, Connection: request.Connection, Repositories: repos})
		if err != nil {
			return nil, err
		}
		cloneURL = matched.CloneURL
		name = matched.FullName
		info = matched
	}
	repo, err := client.Repository(cloneURL)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = repo.URL.FullName()
	}
	return &Opened{Client: client, Repository: repo, Name: name, Info: info}, nil
}

type RepositoryLookup struct {
	Name         string
	Connection   string
	Repositories []git.RepositoryInfo
}

func FindRepository(options RepositoryLookup) (git.RepositoryInfo, error) {
	name, repos := options.Name, options.Repositories
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
	candidates := matches
	message := "is ambiguous"
	if len(matches) == 0 {
		candidates, message = repos, "not found"
	}
	names := make([]string, len(candidates))
	for index, repo := range candidates {
		names[index] = repo.FullName
	}
	sort.Strings(names)
	if len(matches) == 0 {
		return git.RepositoryInfo{}, fmt.Errorf("%w: repository %q %s in connection %q; candidates: %s", ErrInvalid, name, message, options.Connection, strings.Join(names, ", "))
	}
	return git.RepositoryInfo{}, fmt.Errorf("%w: repository %q %s in connection %q: %s; use the full name", ErrInvalid, name, message, options.Connection, strings.Join(names, ", "))
}

func resolve(ctx context.Context, repo *git.Repository, ref string) (git.Ref, error) {
	if strings.TrimSpace(ref) == "" {
		return git.Ref{}, fmt.Errorf("%w: ref is required", ErrInvalid)
	}
	resolved, err := repo.Resolve(ctx, ref)
	if err != nil {
		return git.Ref{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return resolved, nil
}

func (s *Service) repositories(ctx context.Context, name string, client *git.Client) ([]git.RepositoryInfo, error) {
	return s.catalogs.get(ctx, memoLoad[[]git.RepositoryInfo]{key: name + "\x00" + client.CacheKey(), metric: "catalogs", owner: ctx, load: func() ([]git.RepositoryInfo, error) {
		return client.Provider.ListRepositories(ctx, git.ListRepositoriesOptions{})
	}})
}

func (s *Service) RemoteRefs(ctx context.Context, repo *git.Repository) (git.RemoteRefs, error) {
	if git.FreshRefs(ctx) {
		return repo.LsRemote(ctx)
	}
	refs, err := s.refs.get(ctx, memoLoad[git.RemoteRefs]{key: repo.CacheKey(), metric: "refs", owner: ctx, load: func() (git.RemoteRefs, error) {
		return repo.LsRemote(ctx)
	}})
	refs.Branches = append([]git.Ref(nil), refs.Branches...)
	refs.Tags = append([]git.Ref(nil), refs.Tags...)
	if refs.Head != nil {
		head := *refs.Head
		refs.Head = &head
	}
	return refs, err
}

func (s *Service) ResolveRef(ctx context.Context, repo *git.Repository, name string) (git.Ref, error) {
	if err := ctx.Err(); err != nil {
		return git.Ref{}, err
	}
	if ref, err := (git.RemoteRefs{}).Resolve(name); err == nil && ref.Kind == git.RefSHA {
		return ref, nil
	}
	refs, err := s.RemoteRefs(ctx, repo)
	if err != nil {
		return git.Ref{}, err
	}
	ref, err := refs.Resolve(name)
	if err != nil {
		s.refs.delete(repo.CacheKey())
		return ref, errors.Join(err, repo.InvalidateRefs(ctx))
	}
	return ref, nil
}
