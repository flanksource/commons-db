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

// ProviderRequest is the resolved input handed to a Provider by the engine.
type ProviderRequest struct {
	// Connection references a connection (connection://name) or an inline DSN/URL.
	Connection string

	// Query is the provider-native query string.
	Query string

	// Options carries provider-specific knobs from ProviderConfig.Options.
	Options map[string]any
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
