package browse

import (
	"context"
	"fmt"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
)

// CacheRepository is a git cache entry with the catalog connection and
// repository it caches. Connection is empty when no catalog connection serves
// the entry; Repo is empty for a connection pinned to one repository.
type CacheRepository struct {
	git.CacheEntry
	Connection string `json:"connection,omitempty"`
	Repo       string `json:"repo,omitempty"`
}

// CacheInventory is every git cache entry. ConnectionErrors names the catalog
// connections whose repositories could not be listed, so entries they serve
// show no connection.
type CacheInventory struct {
	Entries          []CacheRepository `json:"entries"`
	ConnectionErrors map[string]string `json:"connectionErrors,omitempty"`
}

// CacheAction names what a cache action applies to: Owner (a cache key) for
// prune and evict; Connection and Repo, plus an optional Owner, for rebind.
type CacheAction struct {
	Owner      string `json:"owner,omitempty"`
	Connection string `json:"connection,omitempty"`
	Repo       string `json:"repo,omitempty"`
}

// Cache describes the git cache, joined to the catalog's repositories.
func (s *Service) Cache(ctx context.Context) (CacheInventory, error) {
	entries, err := s.cache.Inventory(ctx)
	if err != nil {
		return CacheInventory{}, err
	}
	sources, failures, err := s.cacheSources(ctx)
	if err != nil {
		return CacheInventory{}, err
	}
	inventory := CacheInventory{Entries: make([]CacheRepository, len(entries)), ConnectionErrors: failures}
	for i, entry := range entries {
		inventory.Entries[i] = CacheRepository{CacheEntry: entry, Connection: sources[entry.Key].Connection, Repo: sources[entry.Key].Repo}
	}
	return inventory, nil
}

// cacheSources maps the cache key of every catalog repository to the first
// connection (by name) serving it.
func (s *Service) cacheSources(ctx context.Context) (map[string]CacheAction, map[string]string, error) {
	connections, err := s.Connections(ctx)
	if err != nil {
		return nil, nil, err
	}
	sources, failures := map[string]CacheAction{}, map[string]string{}
	for _, connection := range connections {
		keys, err := s.connectionCacheKeys(ctx, connection.Name)
		if err != nil {
			failures[connection.Name] = git.Redact(err.Error())
			continue
		}
		for key, repo := range keys {
			if _, seen := sources[key]; !seen {
				sources[key] = CacheAction{Connection: connection.Name, Repo: repo}
			}
		}
	}
	if len(failures) == 0 {
		failures = nil
	}
	return sources, failures, nil
}

func (s *Service) connectionCacheKeys(ctx context.Context, name string) (map[string]string, error) {
	client, connection, err := s.client(ctx, name)
	if err != nil {
		return nil, err
	}
	if client.Type == models.ConnectionTypeGit {
		repo, err := client.Repository(connection.URL)
		if err != nil {
			return nil, err
		}
		return map[string]string{repo.URL.CacheKey(): ""}, nil
	}
	repos, err := s.repositories(ctx, name, client)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]string, len(repos))
	for _, info := range repos {
		repo, err := client.Repository(info.CloneURL)
		if err != nil {
			return nil, fmt.Errorf("repository %s of connection %s: %w", info.FullName, name, err)
		}
		keys[repo.URL.CacheKey()] = info.FullName
	}
	return keys, nil
}

// CacheOwners ranks the mirrors the request's repository could bind to.
func (s *Service) CacheOwners(ctx context.Context, request Request) ([]git.SharedOwner, error) {
	_, repo, err := s.open(ctx, request)
	if err != nil {
		return nil, err
	}
	return s.cache.SharedOwners(ctx, repo)
}

// CachePrune expires the owner mirror's unused pins and garbage-collects it,
// returning its refreshed entry.
func (s *Service) CachePrune(ctx context.Context, action CacheAction) (git.CacheEntry, error) {
	if s.options.PinTTL <= 0 {
		return git.CacheEntry{}, fmt.Errorf("prune git cache: the git browser has no pin TTL configured")
	}
	if action.Owner == "" {
		return git.CacheEntry{}, fmt.Errorf("%w: owner is required", ErrInvalid)
	}
	if err := s.cache.Maintain(ctx, git.MaintainOptions{PinTTL: s.options.PinTTL, Owners: []string{action.Owner}}); err != nil {
		return git.CacheEntry{}, err
	}
	entries, err := s.cache.Inventory(ctx)
	if err != nil {
		return git.CacheEntry{}, err
	}
	for _, entry := range entries {
		if entry.Key == action.Owner {
			return entry, nil
		}
	}
	return git.CacheEntry{}, fmt.Errorf("prune git cache: %s vanished while pruning", action.Owner)
}

// CacheEvict deletes the owner mirror; it and its dependents clone again on
// their next read.
func (s *Service) CacheEvict(ctx context.Context, action CacheAction) (git.EvictResult, error) {
	if action.Owner == "" {
		return git.EvictResult{}, fmt.Errorf("%w: owner is required", ErrInvalid)
	}
	return s.cache.Evict(ctx, git.EvictOptions{Owner: action.Owner})
}

// CacheRebind binds the action's repository to action.Owner, or to the best
// shared owner when Owner is empty.
func (s *Service) CacheRebind(ctx context.Context, action CacheAction) (git.RebindResult, error) {
	_, repo, err := s.open(ctx, Request{Connection: action.Connection, Repo: action.Repo})
	if err != nil {
		return git.RebindResult{}, err
	}
	return s.cache.Rebind(ctx, repo, git.RebindOptions{Owner: action.Owner})
}
