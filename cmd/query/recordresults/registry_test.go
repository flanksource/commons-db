package recordresults_test

import (
	"context"
	"math"
	"path/filepath"
	"time"

	"github.com/flanksource/clicky/cache"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/kv"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

type seqEvent struct {
	Seq int `json:"seq"`
}

type untimedEvent struct {
	At string `json:"at"`
}

var _ profiles.VirtualStore = (*recordresults.Registry)(nil)

func newRegistry() (*recordresults.Registry, *sqlite.Backend) {
	registry, _, index := newRegistryBackends()
	return registry, index
}

func newRegistryBackends() (*recordresults.Registry, recordstore.Backend, *sqlite.Backend) {
	schemas := recordstore.NewSchemas()
	index, err := sqlite.Open(sqlite.Options{
		Path: filepath.Join(GinkgoT().TempDir(), "index.sqlite"), Schema: schemas.Columns, Derived: true,
		SweepInterval: time.Minute,
	})
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(index.Close)
	source, err := kv.New(kv.Options{Store: cache.NewMemory(), Prefix: "records", TTL: time.Hour, MaxChunkBytes: 1 << 20})
	Expect(err).ToNot(HaveOccurred())
	registry, err := recordresults.NewRegistry(recordresults.RegistryOptions{
		Prefix: "trace-results", Schemas: schemas, Index: index, Source: source, ConnectionName: "index",
	})
	Expect(err).ToNot(HaveOccurred())
	return registry, source, index
}

var _ = Describe("Registry", func() {
	ctx := context.Background()

	It("declares one read-only sql profile per result type, paged by seq over the index connection", func() {
		registry, _ := newRegistry()
		Expect(recordresults.RegisterResultType(registry, recordresults.ResultType[sampleEvent]{
			Kind: "sample_event", Title: "Sample events", TimeColumn: "at",
		})).To(Succeed())

		profile, err := registry.Get(ctx, "trace-results/sample_event")
		Expect(err).ToNot(HaveOccurred())
		Expect(profile.Presenter).ToNot(BeNil())
		Expect(profile.Query).To(ContainSubstring(`WHERE "c0" = {{.params.stream}} AND "c1" > {{.params.from}} AND "c1" <= {{.params.to}}`))
		profile.Query, profile.Columns, profile.Presenter = "", nil, nil
		Expect(profile).To(Equal(query.Profile{
			Name: "trace-results/sample_event", Virtual: true, ReadOnly: true,
			Provider: query.ProviderConfig{Type: "sqlite", Connection: "connection://trace-results/index"},
			Params: []query.ParamDef{
				{Name: "stream", Label: "Stream", Required: true, Description: "The record stream to read"},
				{Name: "from", Label: "After seq", Type: query.ParamTypeNumber, Default: int64(0), Description: "Read the rows after this seq"},
				{Name: "to", Label: "Through seq", Type: query.ParamTypeNumber, Default: int64(math.MaxInt64), Description: "Read the rows up to and including this seq"},
			},
			Order:  query.Order{{Column: "at", Desc: true}, {Column: "seq", Unique: true}},
			Limits: &query.RowLimits{PageSize: 100, MaxPageSize: 500, MaxExportRows: recordresults.MaxExportRows},
			Output: []string{"table", "json", "ndjson", "yaml", "csv", "markdown", "html", "excel", "pdf"},
		}))
		Expect(registry.ResultTypes()).To(Equal([]recordresults.RegisteredResultType{
			{Kind: "sample_event", Title: "Sample events", Profile: "trace-results/sample_event"},
		}))
	})

	It("marks the time column as the table's timestamp", func() {
		registry, _ := newRegistry()
		Expect(recordresults.RegisterResultType(registry, recordresults.ResultType[sampleEvent]{
			Kind: "sample_event", Title: "Sample events", TimeColumn: "at",
		})).To(Succeed())
		profile, err := registry.Get(ctx, "trace-results/sample_event")
		Expect(err).ToNot(HaveOccurred())
		Expect(profile.Columns[0]).To(Equal(query.ColumnDef{Name: "seq", Label: "Seq", Type: query.ColumnTypeNumber}))
		Expect(profile.Columns[1]).To(Equal(query.ColumnDef{
			Name: "at", Label: "Captured", Type: query.ColumnTypeDateTime, Kind: query.ColumnKindTimestamp,
		}))
	})

	DescribeTable("refuses a result type it could not serve",
		func(register func(*recordresults.Registry) error, message string) {
			registry, _ := newRegistry()
			Expect(register(registry)).To(MatchError(ContainSubstring(message)))
		},
		Entry("a time column the type does not have", func(r *recordresults.Registry) error {
			return recordresults.RegisterResultType(r, recordresults.ResultType[sampleEvent]{Kind: "k", Title: "K", TimeColumn: "when"})
		}, `time column "when"`),
		Entry("a time column that is not a datetime", func(r *recordresults.Registry) error {
			return recordresults.RegisterResultType(r, recordresults.ResultType[untimedEvent]{Kind: "k", Title: "K", TimeColumn: "at"})
		}, "datetime"),
		Entry("a field named after a column every stream table reserves", func(r *recordresults.Registry) error {
			return recordresults.RegisterResultType(r, recordresults.ResultType[seqEvent]{Kind: "k", Title: "K"})
		}, `"seq"`),
		Entry("no title", func(r *recordresults.Registry) error {
			return recordresults.RegisterResultType(r, recordresults.ResultType[sampleEvent]{Kind: "k"})
		}, "title"),
		Entry("a kind registered twice", func(r *recordresults.Registry) error {
			Expect(recordresults.RegisterResultType(r, recordresults.ResultType[sampleEvent]{Kind: "k", Title: "K"})).To(Succeed())
			return recordresults.RegisterResultType(r, recordresults.ResultType[sampleEvent]{Kind: "k", Title: "K"})
		}, "already registered"),
	)

	It("refuses every write to its profiles", func() {
		registry, _ := newRegistry()
		Expect(recordresults.RegisterResultType(registry, recordresults.ResultType[sampleEvent]{Kind: "k", Title: "K"})).To(Succeed())
		Expect(registry.Save(ctx, query.Profile{Name: "trace-results/k"})).To(MatchError(ContainSubstring("read-only")))
		Expect(registry.Update(ctx, "trace-results/k", query.Profile{}, profiles.UpdateOptions{})).To(MatchError(ContainSubstring("read-only")))
		Expect(registry.Delete(ctx, "trace-results/k")).To(MatchError(ContainSubstring("read-only")))
		Expect(registry.IsVirtual("trace-results/k")).To(BeTrue())
		Expect(registry.IsVirtual("other")).To(BeFalse())
	})

	It("resolves its own index connection by its namespaced reference and its id", func() {
		registry, index := newRegistry()
		connection, err := registry.ResolveConnection("connection://trace-results/index")
		Expect(err).ToNot(HaveOccurred())
		Expect(connection).ToNot(BeNil())
		Expect(connection.Type).To(Equal(models.ConnectionTypeSQLite))
		Expect(connection.URL).To(Equal(index.ReadDSN()))

		byID, err := registry.ResolveConnection(connection.ID.String())
		Expect(err).ToNot(HaveOccurred())
		Expect(byID).To(Equal(connection))
	})

	// "index" is a name any deployment could give a connection of its own;
	// answering to it bare would read that connection's queries against the
	// record index.
	DescribeTable("leaves every reference that is not namespaced to it alone",
		func(reference string) {
			registry, _ := newRegistry()
			connection, err := registry.ResolveConnection(reference)
			Expect(err).ToNot(HaveOccurred())
			Expect(connection).To(BeNil())
		},
		Entry("its bare name", "index"),
		Entry("its name without the namespace", "connection://index"),
		Entry("its name in another namespace", "connection://other/index"),
		Entry("another connection", "connection://elsewhere"),
	)

	It("prepares nothing for a profile it does not own", func() {
		registry, _ := newRegistry()
		release, err := registry.BeforeExecute(ctx, []profiles.ReadRequest{{Profile: query.Profile{Name: "logs"}}})
		Expect(err).ToNot(HaveOccurred())
		Expect(release).ToNot(BeNil())
		release()
	})

	It("prepares every owned stream as one batch and returns one release for the reads", func() {
		registry, source, index := newRegistryBackends()
		Expect(recordresults.RegisterResultType(registry, recordresults.ResultType[sampleEvent]{
			Kind: "sample_event", Title: "Sample events", TimeColumn: "at",
		})).To(Succeed())
		for _, stream := range []string{"run-1", "run-2"} {
			_, err := recordstore.AppendTyped(ctx, source, stream, "sample_event", sampleEvents(1, 2))
			Expect(err).ToNot(HaveOccurred())
		}
		profile, err := registry.Get(ctx, "trace-results/sample_event")
		Expect(err).ToNot(HaveOccurred())
		release, err := registry.BeforeExecute(ctx, []profiles.ReadRequest{
			{Profile: profile, Params: map[string]any{"stream": "run-1"}},
			{Profile: profile, Params: map[string]any{"stream": "run-2"}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(release).ToNot(BeNil())
		for _, stream := range []string{"run-1", "run-2"} {
			meta, err := index.Meta(ctx, stream)
			Expect(err).ToNot(HaveOccurred())
			Expect(meta.HighSeq).To(Equal(int64(2)))
		}
		release()
	})

	DescribeTable("refuses options it cannot run with",
		func(mutate func(*recordresults.RegistryOptions), message string) {
			schemas := recordstore.NewSchemas()
			index, err := sqlite.Open(sqlite.Options{
				Path: filepath.Join(GinkgoT().TempDir(), "i.sqlite"), Schema: schemas.Columns, SweepInterval: time.Minute,
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(index.Close)
			options := recordresults.RegistryOptions{Prefix: "p", Schemas: schemas, Index: index, Source: index, ConnectionName: "c"}
			mutate(&options)
			_, err = recordresults.NewRegistry(options)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("no prefix", func(o *recordresults.RegistryOptions) { o.Prefix = "" }, "prefix"),
		Entry("a prefix that is not a path segment", func(o *recordresults.RegistryOptions) { o.Prefix = "a/b" }, "prefix"),
		Entry("no schemas", func(o *recordresults.RegistryOptions) { o.Schemas = nil }, "schemas"),
		Entry("no index", func(o *recordresults.RegistryOptions) { o.Index = nil }, "index"),
		Entry("no source", func(o *recordresults.RegistryOptions) { o.Source = nil }, "source"),
		Entry("no connection name", func(o *recordresults.RegistryOptions) { o.ConnectionName = "" }, "connection"),
	)
})
