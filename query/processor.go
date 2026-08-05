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

// PageProcessor is an optional Processor capability: a processor whose output
// for a row depends only on that row can run on one page at a time, so a
// profile using nothing else can still be served page by page.
//
// A processor that does not implement it is not deficient — a merge or a
// reconcile genuinely needs every row before any row is correct. It just means
// the profile answering with it has to run its query in full, which is a cost
// worth being able to name rather than discover.
type PageProcessor interface {
	Processor
	ProcessPage(ctx context.Context, spec ProcessorSpec, page Page) (Page, error)
}

// StreamableProcessors reports whether every processor in specs can run page by
// page.
func StreamableProcessors(specs []ProcessorSpec) (bool, error) {
	for index, spec := range specs {
		resolved, err := spec.Resolve()
		if err != nil {
			return false, fmt.Errorf("processor %d: %w", index, err)
		}
		p, err := GetProcessor(resolved.Type)
		if err != nil {
			return false, err
		}
		if _, ok := p.(PageProcessor); !ok {
			return false, nil
		}
	}
	return true, nil
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

// applyProcessors runs the Result through each processor in order, expanding
// library references first.
func applyProcessors(ctx context.Context, specs []ProcessorSpec, result *Result) (*Result, error) {
	for index, spec := range specs {
		resolved, err := spec.Resolve()
		if err != nil {
			return nil, fmt.Errorf("processor %d: %w", index, err)
		}
		p, err := GetProcessor(resolved.Type)
		if err != nil {
			return nil, err
		}
		result, err = p.Process(ctx, resolved, result)
		if err != nil {
			return nil, fmt.Errorf("processor %q: %w", resolved.Label(), err)
		}
	}
	return result, nil
}

// Label names the processor in errors: the library entry when there is one,
// since that is what the author wrote.
func (s ProcessorSpec) Label() string {
	if s.Use != "" {
		return s.Use
	}
	return s.Type
}
