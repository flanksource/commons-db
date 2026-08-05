package query

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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

type FilterOption struct {
	Value string
	Count int64
}

// FilterLookupProvider resolves distinct backend values for one bound column.
type FilterLookupProvider interface {
	LookupFilterValues(ctx context.Context, req ProviderRequest, binding ColumnFilterBinding, search string, limit int) ([]FilterOption, int, error)
}

// ProviderRequest is the resolved input handed to a Provider by the engine.
type ProviderRequest struct {
	// Connection references a connection (connection://name) or an inline DSN/URL.
	Connection string

	// Query is the provider-native query string.
	Query string

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
}

var providerRegistry = map[string]Provider{}

// RegisterProvider adds p to the global provider registry, keyed by p.Type().
// A later registration for the same type replaces the earlier one.
func RegisterProvider(p Provider) {
	providerRegistry[p.Type()] = p
}

// GetProvider returns the registered Provider for typ, or an error listing the
// available types when none is registered.
func GetProvider(typ string) (Provider, error) {
	p, ok := providerRegistry[typ]
	if !ok {
		return nil, fmt.Errorf("no data provider registered for type %q (available: %s)",
			typ, strings.Join(RegisteredProviders(), ", "))
	}
	return p, nil
}

// RegisteredProviders returns the registered provider types, sorted.
func RegisteredProviders() []string {
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
