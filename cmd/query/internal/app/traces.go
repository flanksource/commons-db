// The trace plugins a serving process runs: serve opens their results store,
// and from then on their result profiles and index connection join the catalog.

package app

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/cmd/query/traceserve"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query/profilestore"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/goprof"
	"github.com/flanksource/commons-db/tracing/http"
	"github.com/flanksource/commons-db/tracing/httpproxy"
	"github.com/flanksource/commons-db/tracing/opensearch"
	"github.com/flanksource/commons-db/tracing/pgproxy"
	"github.com/flanksource/commons-db/tracing/process"
	"github.com/flanksource/commons-db/tracing/sqlserver/xevent"
	"github.com/flanksource/commons-db/tracing/sqlstatements"
	"github.com/flanksource/commons-db/tracing/stacks"
	"github.com/flanksource/commons-db/tracing/terminal"
)

// traceKinds is the trace plugin kinds a server serves.
func traceKinds() (*tracing.Kinds, error) {
	kinds := tracing.NewKinds()
	sampler := process.NewSampler()
	dumpers, err := stacks.NewDumpers(stacks.InProcess(), stacks.Native(stacks.NativeOptions{}))
	if err != nil {
		return nil, err
	}
	for name, plugin := range map[string]tracing.TracePlugin{
		"http":                http.Kind(),
		"opensearch":          opensearch.Kind(),
		sqlstatements.RowKind: sqlstatements.Kind(),
		"sql_xevent":          xevent.Kind(),
		process.TreeKind:      process.NewTreeKind(sampler, process.TreeOptions{Dumpers: dumpers}),
		process.SamplesKind:   process.NewSamplesKind(sampler),
		stacks.Kind:           stacks.NewKind(dumpers),
		// Serve knows no process's pprof or gops address: it reads profile
		// files, and the goprof streams of a host's store.
		goprof.Kind:    goprof.NewKind(nil),
		httpproxy.Kind: httpproxy.NewKind(),
		pgproxy.Kind:   pgproxy.NewKind(),
		// A terminal capture records only what a host feeds it, which serve
		// never does; it is served so the streams of a host's store
		// (--traces-dir) read and export.
		terminal.Kind: terminal.NewKind(),
	} {
		if err := kinds.RegisterKind(name, plugin); err != nil {
			return nil, err
		}
	}
	return kinds, nil
}

// tracePlugins holds the trace runtime serve opens. Until it is open the
// profile catalog, profile reads and connection resolution pass it by, so a
// command that never serves never opens a trace store.
type tracePlugins struct {
	mu      sync.RWMutex
	runtime *tracing.Runtime
}

// tracesDir is the trace results store a server opens: the one the host names,
// such as a gavel run's, or else the one under its config dir.
func tracesDir(configDir, named string) string {
	if named != "" {
		return named
	}
	return filepath.Join(configDir, "traces")
}

// open opens the trace results store in dir, declaring kinds' result types,
// and returns the runtime that starts their captures with the close that
// stops using the store. It is the store a host such as gavel serves through
// traceserve, opened the same way.
func (t *tracePlugins) open(dir string, retention time.Duration, kinds *tracing.Kinds) (*tracing.Runtime, func() error, error) {
	runtime, closeStore, err := traceserve.Open(traceserve.OpenOptions{Dir: dir, Kinds: kinds, Retention: retention})
	if err != nil {
		return nil, nil, err
	}
	t.mu.Lock()
	t.runtime = runtime
	t.mu.Unlock()
	return runtime, closeStore, nil
}

// current is the open runtime, or nil before serve opens one.
func (t *tracePlugins) current() *tracing.Runtime {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.runtime
}

// overlay adds the trace result profiles to store once the runtime is open.
func (t *tracePlugins) overlay(store profilestore.Store) (profilestore.Store, error) {
	runtime := t.current()
	if runtime == nil {
		return store, nil
	}
	return profiles.NewOverlayStore(store, runtime.Results.Registry)
}

// beforeExecute catches the trace streams a read names up into the index once
// the runtime is open, and passes every other read through.
func (t *tracePlugins) beforeExecute(ctx context.Context, reads []profilestore.ReadRequest) (func(), error) {
	runtime := t.current()
	if runtime == nil {
		return func() {}, nil
	}
	return runtime.Results.Registry.BeforeExecute(ctx, reads)
}

// resolveConnection resolves the trace index connection once the runtime is
// open, and every other reference through next.
func (t *tracePlugins) resolveConnection(next dbcontext.ConnectionResolver) dbcontext.ConnectionResolver {
	return func(ctx dbcontext.Context, reference string) (*models.Connection, error) {
		connection, err := next(ctx, reference)
		if connection != nil || err != nil {
			return connection, err
		}
		if runtime := t.current(); runtime != nil {
			return runtime.Results.Registry.ResolveConnection(ctx, reference)
		}
		return nil, nil
	}
}
