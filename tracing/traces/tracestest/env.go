// Package tracestest runs trace plugins in specs: a runtime over a real sqlite
// results store and session registry, and helpers that read a session's rows.
package tracestest

import (
	"context"
	"encoding/json"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/probe"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"

	"github.com/flanksource/commons-db/tracing/traces"
)

// Env is a runtime serving kinds over a sqlite results store in the spec's
// own temporary directory, whose sessions all stop when the spec ends.
type Env struct {
	// Dir holds every file the results store writes.
	Dir      string
	Results  *recordresults.Results
	Sessions *query.SessionRegistry
	Runtime  *traces.Runtime
}

// NewEnv serves kinds, committing every 20ms unless configure says otherwise.
func NewEnv(kinds map[string]traces.TracePlugin, configure ...func(*traces.Runtime)) Env {
	catalog := traces.NewKinds()
	for name, plugin := range kinds {
		gomega.Expect(catalog.RegisterKind(name, plugin)).To(gomega.Succeed())
	}
	settings := recordresultstest.LocalSettings(recordstore.BackendSQLite)
	results := recordresultstest.OpenResults(recordresults.OpenOptions{
		Prefix: "traces", ConnectionName: "traces", Settings: settings, Register: catalog.RegisterResultTypes,
	})
	runtime := &traces.Runtime{
		Kinds: catalog, Results: results, Probes: probe.NewManager(probe.ManagerOptions{}), PollEvery: 20 * time.Millisecond,
	}
	for _, apply := range configure {
		apply(runtime)
	}
	runtime.Sessions = query.NewSessionRegistry(query.RegistryOptions{
		Restarters: map[string]query.RestartFunc{
			traces.ProfilePrefix: runtime.Restarter(func() dbcontext.Context { return dbcontext.New() }),
		},
	})
	ginkgo.DeferCleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		gomega.Expect(runtime.Sessions.StopAll(ctx)).To(gomega.Succeed())
	})
	return Env{Dir: settings.Dir, Results: results, Sessions: runtime.Sessions, Runtime: runtime}
}

// Start starts a capture of kind with params, which the kind must accept.
func (e Env) Start(kind, params string) *query.Session {
	managed, err := e.Runtime.Start(dbcontext.New(), traces.StartRequest{Kind: kind, Params: json.RawMessage(params)})
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	return managed.Session()
}

// Running waits for session to run.
func Running(session *query.Session) {
	gomega.Eventually(func() query.SessionState { return session.Snapshot().State }).
		WithTimeout(5 * time.Second).Should(gomega.Equal(query.SessionRunning))
}

// Ended waits for session to end and returns its final record.
func Ended(session *query.Session) query.SessionInfo {
	gomega.Eventually(session.Done()).WithTimeout(10 * time.Second).Should(gomega.BeClosed())
	return session.Snapshot()
}

// Rows reads every row the session committed.
func (e Env) Rows(info query.SessionInfo) []recordstore.Row {
	gomega.Expect(info.Events).ToNot(gomega.BeNil())
	var rows []recordstore.Row
	gomega.Expect(e.Results.Backend.Scan(context.Background(), info.Events.Stream, 0, func(_ int64, row recordstore.Row) error {
		rows = append(rows, row)
		return nil
	})).To(gomega.Succeed())
	return rows
}

// Sealed reports whether the session's stream is sealed.
func (e Env) Sealed(info query.SessionInfo) bool {
	meta, err := e.Results.Backend.Meta(context.Background(), info.Events.Stream)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	return meta.Sealed
}

// Summary decodes what the session's capture reported emitting.
func Summary(info query.SessionInfo) traces.Summary {
	var summary struct {
		Source traces.Summary `json:"source"`
	}
	gomega.Expect(json.Unmarshal(info.Summary, &summary)).To(gomega.Succeed())
	return summary.Source
}
