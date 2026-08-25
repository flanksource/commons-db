package query

import (
	"fmt"
	"iter"
	"maps"
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

// PageProcessor is an optional Processor capability: a processor that can run
// on one page at a time, so a profile using nothing else can still be served
// page by page.
//
// state is what this processor returned from the previous page of the same
// walk, and is nil on the first. It travels inside the cursor, which is the only
// thing a resumed request carries — so a processor that folds rows across the
// whole result can still run incrementally by remembering what it has already
// emitted. Whatever it returns is handed back on the next page; returning nil
// keeps the processor stateless, which is all a per-row transform needs.
//
// A processor that does not implement this is not deficient — a merge or a
// reconcile genuinely needs every row before any row is correct. It just means
// the profile answering with it has to run its query in full, which is a cost
// worth being able to name rather than discover.
type PageProcessor interface {
	Processor
	ProcessPage(ctx context.Context, spec ProcessorSpec, page Page, state []byte) (Page, []byte, error)
}

// StreamableProcessors reports whether every processor in specs can run page by
// page.
func StreamableProcessors(specs []ProcessorSpec) (bool, error) {
	label, err := nonPageProcessor(specs)
	return label == "", err
}

func nonPageProcessor(specs []ProcessorSpec) (string, error) {
	for index, spec := range specs {
		resolved, err := spec.Resolve()
		if err != nil {
			return "", fmt.Errorf("processor %d: %w", index, err)
		}
		registered, err := GetProcessor(resolved.Type)
		if err != nil {
			return "", err
		}
		if _, ok := registered.(PageProcessor); !ok {
			return resolved.Label(), nil
		}
	}
	return "", nil
}

type pageProcessorStage struct {
	label     string
	spec      ProcessorSpec
	processor PageProcessor
}

type pageProcessorChain struct {
	stages []pageProcessorStage
	state  map[string][]byte
}

func newPageProcessorChain(specs []ProcessorSpec, carried map[string][]byte) (*pageProcessorChain, error) {
	chain := &pageProcessorChain{state: maps.Clone(carried)}
	if chain.state == nil {
		chain.state = map[string][]byte{}
	}
	for index, spec := range specs {
		resolved, err := spec.Resolve()
		if err != nil {
			return nil, fmt.Errorf("processor %d: %w", index, err)
		}
		registered, err := GetProcessor(resolved.Type)
		if err != nil {
			return nil, err
		}
		processor, ok := registered.(PageProcessor)
		if !ok {
			return nil, fmt.Errorf("processor %q cannot run page by page", resolved.Label())
		}
		chain.stages = append(chain.stages, pageProcessorStage{
			label: resolved.Label(), spec: resolved, processor: processor,
		})
	}
	return chain, nil
}

func (c *pageProcessorChain) Process(ctx context.Context, page Page) (Page, error) {
	for _, stage := range c.stages {
		var next []byte
		var err error
		page, next, err = stage.processor.ProcessPage(ctx, stage.spec, page, c.state[stage.label])
		if err != nil {
			return Page{}, fmt.Errorf("processor %q: %w", stage.label, err)
		}
		if len(next) == 0 {
			delete(c.state, stage.label)
		} else {
			c.state[stage.label] = next
		}
	}
	return page, nil
}

// ProcessPages applies a streamable processor chain to each page while
// preserving its cursor, totals and provider metadata.
//
// carried is the state each processor left on the previous page, keyed by
// label, and each page's own state is written back onto Page.State for the
// cursor to carry. ExecutePages calls this before the cursor is minted; a
// preview or a sample calls it with no carried state because it reads one
// bounded batch rather than walking.
func ProcessPages(
	ctx context.Context,
	specs []ProcessorSpec,
	carried map[string][]byte,
	pages iter.Seq2[Page, error],
) iter.Seq2[Page, error] {
	chain, err := newPageProcessorChain(specs, carried)
	if err != nil {
		return ErrorPage(err)
	}

	return func(yield func(Page, error) bool) {
		for page, err := range pages {
			if err != nil {
				yield(Page{}, err)
				return
			}
			page, err = chain.Process(ctx, page)
			if err != nil {
				yield(Page{}, err)
				return
			}
			// Only a page that resumes carries state forward; the last page of a
			// walk has nowhere to carry it to.
			if len(chain.state) > 0 && page.HasMore {
				page.State = maps.Clone(chain.state)
			}
			if !yield(page, nil) {
				return
			}
		}
	}
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
		if result == nil {
			return nil, fmt.Errorf("processor %q returned a nil result", resolved.Label())
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
