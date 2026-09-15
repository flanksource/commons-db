package recordresults

import (
	"context"
	"fmt"
	"iter"
	"sync"

	"github.com/google/uuid"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

const (
	// ProviderType is the provider a followable result type's profile reads
	// through (ResultType.Follow). It pages, filters, looks values up and exports
	// exactly as the sqlite provider it delegates to, and it also streams: a
	// session started with follow=true tails the stream it names.
	ProviderType = "recordstore"

	// indexProviderType is the provider every other result profile reads its
	// index through.
	indexProviderType = "sqlite"
)

func init() { query.RegisterProvider(followProvider{}) }

// registrySet is every registry a follow can resolve, by its index connection's
// id: a provider is registered once per process, but each registry serves its
// own index and source.
type registrySet struct {
	mu         sync.RWMutex
	registries map[uuid.UUID]*Registry
}

var followRegistries = &registrySet{registries: map[uuid.UUID]*Registry{}}

func (s *registrySet) add(registry *Registry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registries[registry.connection.ID] = registry
}

func (s *registrySet) remove(registry *Registry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.registries, registry.connection.ID)
}

// resolve is the registry whose index connection reference names, resolved
// through the connection resolver ctx carries — the registry's own
// ResolveConnection, installed where the profile engine reads.
func (s *registrySet) resolve(ctx dbcontext.Context, reference string) (*Registry, error) {
	resolved, err := dbcontext.FindConnectionByURL(ctx, reference)
	if err != nil {
		return nil, fmt.Errorf("follow record results: resolve connection %q: %w", reference, err)
	}
	if resolved == nil {
		return nil, fmt.Errorf("follow record results: connection %q not found", reference)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	registry, ok := s.registries[resolved.ID]
	if !ok {
		return nil, fmt.Errorf("follow record results: connection %q is not the index of an open result registry whose source notifies", reference)
	}
	return registry, nil
}

// Close stops the registry answering follows. It closes nothing it was given:
// the index and source belong to whoever opened them.
func (r *Registry) Close() error {
	followRegistries.remove(r)
	return nil
}

// BeforeRead is the query.RegistryOptions.BeforeRead hook for a session
// registry serving result profiles. A host mounts the sessions API over result
// profiles as:
//
//	registry := query.NewSessionRegistry(query.RegistryOptions{BeforeRead: results.Registry.BeforeRead})
//	service, err := sessions.New(sessions.Options{Profiles: ..., Context: ..., Registry: registry})
//	handler, err := service.Handler("/api/v1", profileHandler)
//
// where Profiles overlays results.Registry on the profile store and Context
// carries results.Registry.ResolveConnection, as for the profile service.
//
// It catches the index up and checks the stream exactly as BeforeExecute does
// for a page. A top session keeps the lease for its sample. A trace — a follow —
// gives it back at once: it reads for as long as it lasts, a lease held that
// long would hold every append off, and the follow provider takes its own lease
// around each read.
func (r *Registry) BeforeRead(ctx context.Context, p query.Profile, params map[string]any) (func(), error) {
	release, err := r.BeforeExecute(ctx, []profiles.ReadRequest{{Profile: p, Params: params}})
	if err != nil {
		return nil, err
	}
	if p.Kind() == query.KindTrace {
		release()
		return func() {}, nil
	}
	return release, nil
}

// followProvider serves followable result profiles: every read is the sqlite
// provider's, and Stream tails the stream.
type followProvider struct{}

var (
	_ query.StreamProvider            = followProvider{}
	_ query.PagingProvider            = followProvider{}
	_ query.FilterLookupProvider      = followProvider{}
	_ query.BackendCapabilityProvider = followProvider{}
	_ query.ColumnInspectionProvider  = followProvider{}
	_ query.SortingProvider           = followProvider{}
	_ query.QueryParameterizer        = followProvider{}
)

// indexProvider is the sqlite provider with the capabilities result profiles
// read through.
type indexProvider interface {
	query.PagingProvider
	query.FilterLookupProvider
	query.BackendCapabilityProvider
	query.ColumnInspectionProvider
	query.SortingProvider
	query.QueryParameterizer
}

func sqliteProvider() (indexProvider, error) {
	provider, err := query.GetProvider(indexProviderType)
	if err != nil {
		return nil, fmt.Errorf("%s provider: %w; link github.com/flanksource/commons-db/query/providers", ProviderType, err)
	}
	index, ok := provider.(indexProvider)
	if !ok {
		return nil, fmt.Errorf("%s provider: the registered %q provider cannot page, look up, inspect and sort an index", ProviderType, indexProviderType)
	}
	return index, nil
}

func (followProvider) Type() string { return ProviderType }

// ParameterizeQuery binds the profile's params as the sqlite provider does:
// as placeholders, never into the statement text.
func (followProvider) ParameterizeQuery(request query.QueryParameterizationRequest) (query.ParameterizedQuery, error) {
	index, err := sqliteProvider()
	if err != nil {
		return query.ParameterizedQuery{}, err
	}
	return index.ParameterizeQuery(request)
}

func (followProvider) Execute(ctx dbcontext.Context, req query.ProviderRequest) ([]query.Row, error) {
	index, err := sqliteProvider()
	if err != nil {
		return nil, err
	}
	return index.Execute(ctx, req)
}

func (followProvider) Pages(ctx dbcontext.Context, req query.ProviderRequest, page query.PageRequest) iter.Seq2[query.Page, error] {
	index, err := sqliteProvider()
	if err != nil {
		return query.ErrorPage(err)
	}
	return index.Pages(ctx, req, page)
}

// PagingModes are the sqlite provider's; none when it is not linked, which
// every profile validation reports as a provider that cannot page.
func (followProvider) PagingModes() query.PagingMode {
	return query.SupportsPaging(indexProviderType)
}

func (followProvider) SupportsRequestSort() bool {
	return query.SupportsRequestSort(indexProviderType)
}

func (followProvider) LookupFilterValues(
	ctx dbcontext.Context, req query.ProviderRequest, binding query.ColumnFilterBinding, search string, limit int,
) ([]query.FilterOption, *query.Total, error) {
	index, err := sqliteProvider()
	if err != nil {
		return nil, nil, err
	}
	return index.LookupFilterValues(ctx, req, binding, search, limit)
}

func (followProvider) BackendCapabilities(ctx dbcontext.Context, req query.ProviderRequest) (connection.BackendCapabilities, error) {
	index, err := sqliteProvider()
	if err != nil {
		return connection.BackendCapabilities{}, err
	}
	return index.BackendCapabilities(ctx, req)
}

func (followProvider) InspectColumnFilters(
	ctx dbcontext.Context, req query.ProviderRequest, columns []query.ColumnDef,
) (query.ColumnInspectionResult, error) {
	index, err := sqliteProvider()
	if err != nil {
		return query.ColumnInspectionResult{}, err
	}
	return index.InspectColumnFilters(ctx, req, columns)
}

// Stream tails the stream req names, from its afterSeq, through its registry.
func (followProvider) Stream(ctx dbcontext.Context, req query.ProviderRequest, emit func(query.Row)) error {
	registry, err := followRegistries.resolve(ctx, req.Connection)
	if err != nil {
		return err
	}
	return registry.follow(ctx, req, emit)
}
