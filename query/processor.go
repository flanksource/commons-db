package query

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flanksource/commons-db/context"
)

// Processor is a post-query step applied to a Result (e.g. sqlite merge,
// reconciliation). Implementations self-register via RegisterProcessor and are
// selected by ProcessorSpec.Type. Like providers, processors live in a
// subpackage that consumers blank-import.
type Processor interface {
	// Type is the registry key (e.g. "sqlite.merge", "sqlite.recon").
	Type() string

	// Process transforms in according to spec and returns the new Result.
	Process(ctx context.Context, spec ProcessorSpec, in *Result) (*Result, error)
}

var processorRegistry = map[string]Processor{}

// RegisterProcessor adds p to the global processor registry, keyed by p.Type().
func RegisterProcessor(p Processor) {
	processorRegistry[p.Type()] = p
}

// GetProcessor returns the registered Processor for typ, or an error listing the
// available types.
func GetProcessor(typ string) (Processor, error) {
	p, ok := processorRegistry[typ]
	if !ok {
		return nil, fmt.Errorf("no processor registered for type %q (available: %s)",
			typ, strings.Join(RegisteredProcessors(), ", "))
	}
	return p, nil
}

// RegisteredProcessors returns the registered processor types, sorted.
func RegisteredProcessors() []string {
	types := make([]string, 0, len(processorRegistry))
	for t := range processorRegistry {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// applyProcessors runs the Result through each processor in order.
func applyProcessors(ctx context.Context, specs []ProcessorSpec, result *Result) (*Result, error) {
	for _, spec := range specs {
		p, err := GetProcessor(spec.Type)
		if err != nil {
			return nil, err
		}
		result, err = p.Process(ctx, spec, result)
		if err != nil {
			return nil, fmt.Errorf("processor %q: %w", spec.Type, err)
		}
	}
	return result, nil
}
