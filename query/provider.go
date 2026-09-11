package query

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
)

// Provider executes a Profile's query against a single backend type and returns
// the raw rows. Implementations register themselves via RegisterProvider and are
// selected by ProviderConfig.Type.
type Provider interface {
	// Type is the registry key (e.g. "sql", "http", "prometheus").
	Type() string

	// Execute runs req against the backend and returns the raw rows.
	Execute(ctx context.Context, req ProviderRequest) ([]Row, error)
}

// BackendCapabilityProvider reports features from the live backend selected by
// a request. It is optional because non-SQL providers need not expose SQL
// feature metadata merely to implement Provider.
type BackendCapabilityProvider interface {
	BackendCapabilities(ctx context.Context, req ProviderRequest) (connection.BackendCapabilities, error)
}

func BackendCapabilitiesFor(ctx context.Context, req ProviderRequest) (connection.BackendCapabilities, error) {
	provider, err := GetProvider(req.Provider)
	if err != nil {
		return connection.BackendCapabilities{}, err
	}
	capabilityProvider, ok := provider.(BackendCapabilityProvider)
	if !ok {
		return connection.BackendCapabilities{}, fmt.Errorf("provider %q does not report backend capabilities", req.Provider)
	}
	capabilities, err := capabilityProvider.BackendCapabilities(ctx, req)
	if err != nil {
		return connection.BackendCapabilities{}, fmt.Errorf("get backend capabilities from provider %q: %w", req.Provider, err)
	}
	if err := capabilities.Validate(); err != nil {
		return connection.BackendCapabilities{}, fmt.Errorf("provider %q returned invalid backend capabilities: %w", req.Provider, err)
	}
	return capabilities, nil
}

func RequireBackendCapability(
	ctx context.Context,
	req ProviderRequest,
	capability connection.BackendCapability,
) (connection.BackendCapabilities, error) {
	capabilities, err := BackendCapabilitiesFor(ctx, req)
	if err != nil {
		return connection.BackendCapabilities{}, err
	}
	if err := capabilities.Require(capability); err != nil {
		return connection.BackendCapabilities{}, err
	}
	return capabilities, nil
}

// QueryParameterizer replaces provider-native parameter references with opaque
// markers while carrying their values separately to the backend driver.
type QueryParameterizer interface {
	ParameterizeQuery(request QueryParameterizationRequest) (ParameterizedQuery, error)
}

type QueryParameterizationRequest struct {
	Query       string
	Params      map[string]any
	Definitions []ParamDef
}

type ParameterizedQuery struct {
	Query       string
	Args        []any
	Identifiers []string
	UsedParams  []string
}

type FilterOption struct {
	Value string
	Count int64
}

type InspectionOptions struct {
	Refresh bool
}

// FilterLookupProvider resolves distinct backend values for one bound column.
//
// The total is a *Total rather than an int because not every backend can count
// exactly: a SQL COUNT is the number, an OpenSearch cardinality aggregation is
// an estimate, and nil is "the backend did not say". Collapsing the three into
// an int is what lets an estimate be rendered as a count.
type FilterLookupProvider interface {
	LookupFilterValues(ctx context.Context, req ProviderRequest, binding ColumnFilterBinding, search string, limit int) ([]FilterOption, *Total, error)
}

// ProviderRequest is the resolved input handed to a Provider by the engine.
type ProviderRequest struct {
	// Provider is the resolved registry key. It is carried so optional
	// diagnostics can identify the native backend without re-deriving it.
	Provider string

	// Connection references a connection (connection://name) or an inline DSN/URL.
	Connection string

	// Query is the provider-native query string.
	Query string

	// QueryArgs are values bound to placeholders derived from Query parameter
	// references. They never form part of Query itself.
	QueryArgs []any

	// QueryIdentifiers are validated names represented by opaque markers in
	// Query. The SQL provider quotes them for the resolved connection dialect.
	QueryIdentifiers []string

	// Options carries provider-specific knobs from ProviderConfig.Options.
	Options map[string]any

	// Params contains the validated profile parameters. Providers use this for
	// native query builders that cannot be expressed as a query template.
	Params map[string]any

	// ParamRoles maps each declared param name to its ParamRole, so a structural
	// query builder can fold role-carrying params (time-from, limit, …) into the
	// backend's native constructs instead of treating them as plain filters.
	ParamRoles map[string]ParamRole

	// TemplatedParams names the params consumed while templating Query, Options
	// or Connection. A provider with its own structural param binding counts
	// them as referenced, so a param interpolated into the options is not
	// reported as unused.
	TemplatedParams []string

	// Filters contains native include/exclude clauses bound to profile columns.
	Filters []ColumnFilterValue

	// Order is the Profile's declared result order. A provider that pages by
	// cursor needs it to sort by, and to cut the next position out of the last
	// row it returned.
	Order Order

	// Position is the decoded cursor this request resumes after, empty at the
	// start of a walk. The engine validates and decodes it, so a provider works
	// in key values and never in the token format.
	Position CursorPosition

	// Diagnostics is populated only for an explicitly requested debug run.
	// Providers record their final native request and response details here so
	// failures can return the same evidence as successful executions.
	Diagnostics *ProviderDiagnostics

	Inspection InspectionOptions
}

var (
	providerRegistryMu sync.RWMutex
	providerRegistry   = map[string]Provider{}
)

// RegisterProvider adds p to the global provider registry, keyed by p.Type().
// A later registration for the same type replaces the earlier one.
func RegisterProvider(p Provider) {
	typ := p.Type()
	providerRegistryMu.Lock()
	defer providerRegistryMu.Unlock()
	providerRegistry[typ] = p
}

// GetProvider returns the registered Provider for typ, or an error listing the
// available types when none is registered.
func GetProvider(typ string) (Provider, error) {
	providerRegistryMu.RLock()
	defer providerRegistryMu.RUnlock()
	p, ok := providerRegistry[typ]
	if !ok {
		return nil, fmt.Errorf("no data provider registered for type %q (available: %s)",
			typ, strings.Join(registeredProvidersLocked(), ", "))
	}
	return p, nil
}

// RegisteredProviders returns the registered provider types, sorted.
func RegisteredProviders() []string {
	providerRegistryMu.RLock()
	defer providerRegistryMu.RUnlock()
	return registeredProvidersLocked()
}

func registeredProvidersLocked() []string {
	types := make([]string, 0, len(providerRegistry))
	for t := range providerRegistry {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// DecodeOptions decodes a ProviderRequest.Options map into a provider-specific
// options struct T via a JSON round-trip (T's json tags drive the mapping).
// Returns the zero T when opts is empty.
func DecodeOptions[T any](opts map[string]any) (T, error) {
	var out T
	if len(opts) == 0 {
		return out, nil
	}
	b, err := json.Marshal(opts)
	if err != nil {
		return out, fmt.Errorf("failed to encode provider options: %w", err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("failed to decode provider options: %w", err)
	}
	return out, nil
}
