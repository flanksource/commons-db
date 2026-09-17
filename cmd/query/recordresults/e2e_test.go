package recordresults_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/cache"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/formatters"
	"github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/cmd/query/recordresults"
	dbcontext "github.com/flanksource/commons-db/context"
	_ "github.com/flanksource/commons-db/query/providers"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/kv"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

// sampleEvent is a small result type in the shape a trace capture produces: an
// instant, two low-cardinality dimensions, a measure and a structured detail.
type sampleEvent struct {
	At      time.Time      `json:"at" pretty:"label=Captured"`
	DB      string         `json:"db"`
	User    string         `json:"user"`
	Elapsed float64        `json:"elapsed_ms" pretty:"type=duration,unit=ms"`
	Slow    bool           `json:"slow"`
	Tables  []string       `json:"tables"`
	Detail  map[string]any `json:"detail"`
}

func (sampleEvent) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("at").Label("Captured").Kind("timestamp").Build(),
		api.Column("db").Label("Database").Build(),
		api.Column("user").Label("User").Build(),
		api.Column("elapsed_ms").Label("Elapsed").Build(),
		api.Column("slow").Label("Slow").Build(),
		api.Column("tables").Label("Tables").Kind("tags").Build(),
		api.Column("detail").Hidden().Build(),
	}
}

func (e sampleEvent) Row() map[string]any {
	return map[string]any{
		"at": e.At,
		"db": api.TableCell{
			Value:       api.Text{Content: e.DB, Style: "text-blue-500"},
			FilterValue: e.DB,
		},
		"user": e.User, "elapsed_ms": e.Elapsed, "slow": e.Slow,
		"tables": e.Tables, "detail": e.Detail,
	}
}

var (
	sampleDBs   = []string{"oipa", "audit", "report"}
	sampleUsers = []string{"alice", "bob"}
	sampleStart = time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
)

// sampleEvents are events first..last; event n is n milliseconds after start,
// is slow when n is a multiple of 5, and reads the tables sampleTables(n).
func sampleEvents(first, last int) []sampleEvent {
	events := make([]sampleEvent, 0, last-first+1)
	for n := first; n <= last; n++ {
		events = append(events, sampleEvent{
			At: sampleStart.Add(time.Duration(n) * time.Millisecond), DB: sampleDBs[n%3], User: sampleUsers[n%2],
			Elapsed: float64(n), Slow: n%5 == 0, Tables: sampleTables(n), Detail: map[string]any{"n": n},
		})
	}
	return events
}

// sampleTables is what event n reads: every event reads policy, an even one
// also reads client, and a multiple of 3 also reads activity. Every tenth reads
// nothing at all, which is the row an exclusion must still keep.
func sampleTables(n int) []string {
	if n%10 == 0 {
		return nil
	}
	tables := []string{"policy"}
	if n%2 == 0 {
		tables = append(tables, "client")
	}
	if n%3 == 0 {
		tables = append(tables, "activity")
	}
	return tables
}

type resultServer struct {
	source   *kv.Backend
	registry *recordresults.Registry
	handler  http.Handler
}

func newResultServer() resultServer {
	ctx := context.Background()
	schemas := recordstore.NewSchemas()
	source := newKV(schemas)
	registry := newSampleRegistry(source, schemas)
	_, err := recordstore.AppendTyped(ctx, source, "run-1", "sample_event", sampleEvents(1, 250))
	Expect(err).ToNot(HaveOccurred())
	_, err = recordstore.AppendTyped(ctx, source, "run-2", "sample_event", sampleEvents(1000, 1004))
	Expect(err).ToNot(HaveOccurred())
	return resultServer{source: source, registry: registry, handler: serveResults(registry)}
}

// newSampleRegistry wires the way a server does: a sqlite index resolving
// kinds through the schemas source resolves them through, kept caught up with
// source.
func newSampleRegistry(source recordstore.Backend, schemas *recordstore.Schemas) *recordresults.Registry {
	index, err := sqlite.Open(sqlite.Options{
		Path: filepath.Join(GinkgoT().TempDir(), "index.sqlite"), Schema: schemas.Kind, Derived: true,
		SweepInterval: time.Minute,
	})
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(index.Close)
	registry, err := recordresults.NewRegistry(recordresults.RegistryOptions{
		Prefix: "trace-results", Schemas: schemas, Index: index, Source: source, ConnectionName: "index",
	})
	Expect(err).ToNot(HaveOccurred())
	Expect(recordresults.RegisterResultType(registry, recordresults.ResultType[sampleEvent]{
		Kind: "sample_event", Title: "Sample events", TimeColumn: "at",
	})).To(Succeed())
	return registry
}

// serveResults mounts a profile service the way commons-db's own app mounts
// snapshots: the registry overlaid on the profile store, its connection and
// resolver on the query context, and its hook around every read.
func serveResults(registry *recordresults.Registry) http.Handler {
	return serveService(newResultService(registry))
}

// newResultService is the profile service over registry, the one both the CLI
// actions and the HTTP surface read through.
func newResultService(registry *recordresults.Registry) *profiles.Service {
	base, err := profiles.NewFileStore(GinkgoT().TempDir())
	Expect(err).ToNot(HaveOccurred())
	overlay, err := profiles.NewOverlayStore(base, registry)
	Expect(err).ToNot(HaveOccurred())
	queryCtx := dbcontext.New().WithConnectionResolver(registry.ResolveConnection)
	service, err := profiles.New(profiles.Options{
		Store:         func() (profiles.Store, error) { return overlay, nil },
		Context:       func() dbcontext.Context { return queryCtx },
		DecodeBody:    profiles.DecodeRequestBody,
		BeforeExecute: registry.BeforeExecute,
	})
	Expect(err).ToNot(HaveOccurred())
	return service
}

func serveService(service *profiles.Service) http.Handler {
	service.RegisterFamily()
	DeferCleanup(func() { entity.UnregisterDynamicEntityFamily("profile") })

	root := &cobra.Command{Use: "query"}
	root.AddCommand(&cobra.Command{Use: "version", Run: func(*cobra.Command, []string) {}})
	server := rpc.NewSwaggerServer(
		&rpc.ServeConfig{
			Title: "Query", Version: "0.1.0", SkipHealth: true,
			Executor: &rpc.ExecutorConfig{Enabled: true, SkipPreRun: true, PathPrefix: "/api/v1"},
		},
		root, &rpc.OpenAPIConfig{Title: "Query", Version: "0.1.0"},
	)
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)
	handler, err := service.Handler("/api/v1", mux)
	Expect(err).ToNot(HaveOccurred())
	return handler
}

// profilePath is the escaped path the snapshot descriptors already hand out: a
// profile name holding a "/" is one path segment only once it is escaped.
var profilePath = "/api/v1/profile/" + url.PathEscape("trace-results/sample_event")

func (s resultServer) get(target, accept string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Accept", accept)
	response := httptest.NewRecorder()
	s.handler.ServeHTTP(response, request)
	return response
}

func (s resultServer) rows(query string) ([]map[string]any, http.Header) {
	response := s.get(profilePath+"?"+query, "application/json")
	Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
	var rows []map[string]any
	Expect(json.Unmarshal(response.Body.Bytes(), &rows)).To(Succeed(), response.Body.String())
	return rows, response.Header()
}

func column(rows []map[string]any, name string) []any {
	values := make([]any, len(rows))
	for index, row := range rows {
		values[index] = row[name]
	}
	return values
}

var _ = Describe("a record result type served through the profile engine", Ordered, func() {
	var server resultServer

	BeforeAll(func() { server = newResultServer() })

	It("serves the stream's first page of 100, newest first, with the stream's total", func() {
		rows, header := server.rows("stream=run-1")
		Expect(header.Get("X-Total-Count")).To(Equal("250"))
		Expect(header.Get("X-Has-More")).To(Equal("true"))
		Expect(rows).To(HaveLen(100))
		Expect(rows[0]["seq"]).To(BeEquivalentTo(250))
		Expect(rows[99]["seq"]).To(BeEquivalentTo(151))
		Expect(rows[0]).ToNot(HaveKey("stream_id"))
		Expect(rows[0]["detail"]).To(Equal(map[string]any{"n": float64(250)}))
	})

	It("restores a typed row's rich cells only in the interactive response", func() {
		response := server.get(profilePath+"?stream=run-1&limit=1", "application/json+clicky")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Body.String()).To(And(
			ContainSubstring(`"className": "text-blue-500"`),
			ContainSubstring(`"filterValue": 250`),
			ContainSubstring(`"kind": "tags"`),
			ContainSubstring(`"detail"`),
			ContainSubstring(`{\"n\":250}`),
		))

		raw := server.get(profilePath+"?stream=run-1&limit=1", "application/json")
		Expect(raw.Code).To(Equal(http.StatusOK), raw.Body.String())
		Expect(raw.Body.String()).To(And(ContainSubstring(`"db":"audit"`), Not(ContainSubstring("text-blue-500"))))
	})

	It("presents the seq read back from the index as an integer and the capture time as its zoned instant", func() {
		response := server.get(profilePath+"?stream=run-1&limit=1", "application/json+clicky")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		var document formatters.ClickyDocument
		Expect(json.Unmarshal(response.Body.Bytes(), &document)).To(Succeed())
		Expect(document.Node.Rows).To(HaveLen(1))
		cells := document.Node.Rows[0].Cells

		Expect(cells["seq"].Plain).To(Equal("250"))
		Expect(cells["at"].FilterValue).To(Equal("2026-09-10T06:00:00.25Z"))
	})

	It("reads a boolean column back as a JSON boolean", func() {
		rows, _ := server.rows("stream=run-1&limit=2")
		Expect(column(rows, "slow")).To(Equal([]any{true, false}))
	})

	It("answers on the surface key as well as the escaped name", func() {
		response := server.get("/api/v1/profile/profile-trace-results-sample-event?stream=run-2", "application/json")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Header().Get("X-Total-Count")).To(Equal("5"))
	})

	It("includes and excludes by column filter", func() {
		rows, header := server.rows("stream=run-1&filter.db=oipa,report&filter.user=!bob&limit=500")
		expected := 0
		for _, event := range sampleEvents(1, 250) {
			if event.DB != "audit" && event.User != "bob" {
				expected++
			}
		}
		Expect(header.Get("X-Total-Count")).To(Equal(fmt.Sprint(expected)))
		Expect(rows).To(HaveLen(expected))
		for _, row := range rows {
			Expect(row["db"]).To(BeElementOf("oipa", "report"))
			Expect(row["user"]).To(Equal("alice"))
		}
	})

	It("offers each filterable column's distinct values for the stream", func() {
		response := server.get(
			"/api/v1/profile/profile-trace-results-sample-event?stream=run-1&__lookup=filters", "application/json+clicky")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		var body struct {
			Filters map[string]struct {
				Options map[string]any `json:"options"`
				Total   int            `json:"total"`
			} `json:"filters"`
		}
		Expect(json.Unmarshal(response.Body.Bytes(), &body)).To(Succeed(), response.Body.String())
		db := body.Filters["filter.db"]
		Expect(db.Total).To(Equal(3))
		Expect(db.Options).To(HaveLen(3))
		Expect(db.Options).To(And(HaveKey("oipa"), HaveKey("audit"), HaveKey("report")))
		Expect(body.Filters["filter.user"].Total).To(Equal(2))
	})

	It("pages past the first page by cursor, newest first", func() {
		first, header := server.rows("stream=run-1")
		cursor := header.Get("X-Next-Cursor")
		Expect(cursor).ToNot(BeEmpty())

		second, header := server.rows("stream=run-1&cursor=" + url.QueryEscape(cursor))
		Expect(header.Get("X-Total-Count")).To(Equal("250"))
		Expect(second).To(HaveLen(100))
		Expect(first[99]["seq"]).To(BeEquivalentTo(151))
		Expect(second[0]["seq"]).To(BeEquivalentTo(150))
		Expect(second[99]["seq"]).To(BeEquivalentTo(51))
	})

	// Event n is captured n milliseconds after 06:00:00Z. Each bound is written
	// to a different width or zone than the index stores, which is exactly what a
	// text comparison of instants gets wrong.
	DescribeTable("reads a time window over the index, whatever zone its edges are written in",
		func(window string, firstSeq, lastSeq int) {
			rows, header := server.rows("stream=run-1&limit=500&sort=seq&order=asc&" + window)
			expected := lastSeq - firstSeq + 1
			Expect(header.Get("X-Total-Count")).To(Equal(fmt.Sprint(expected)))
			Expect(rows).To(HaveLen(expected))
			Expect(rows[0]["seq"]).To(BeEquivalentTo(firstSeq))
			Expect(rows[expected-1]["seq"]).To(BeEquivalentTo(lastSeq))
		},
		Entry("from an instant, inclusive", "from="+url.QueryEscape("2026-09-10T06:00:00.1Z"), 100, 250),
		Entry("to an instant written at +02:00, exclusive", "to="+url.QueryEscape("2026-09-10T08:00:00.011+02:00"), 1, 10),
		Entry("a half-open window", "from="+url.QueryEscape("2026-09-10T06:00:00.020Z")+"&to="+url.QueryEscape("2026-09-10T06:00:00.030Z"), 20, 29),
	)

	It("offers no column filter on the time column its window already bounds", func() {
		response := server.get(profilePath+"?stream=run-1&filter.at="+url.QueryEscape(">2026-09-10T06:00:00Z"), "application/json")
		Expect(response.Code).To(Equal(http.StatusBadRequest), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring(`column filter \"filter.at\" is not supported`))
	})

	It("sorts by a requested column ahead of the declared order", func() {
		rows, _ := server.rows("stream=run-1&sort=db&order=asc&limit=500")
		dbs := column(rows, "db")
		Expect(slices.IsSortedFunc(dbs, func(a, b any) int { return strings.Compare(a.(string), b.(string)) })).To(BeTrue())
		Expect(dbs[0]).To(Equal("audit"))
	})

	It("exports every row of the stream as a workbook", func() {
		response := server.get(profilePath+"?stream=run-1&scope=all&format=excel", "")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Header().Get("Content-Type")).To(Equal("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"))
		Expect(response.Body.String()).To(HavePrefix("PK"))
	})

	It("reads only the seq window it is given", func() {
		rows, header := server.rows("stream=run-1&afterSeq=10&toSeq=20&sort=seq&order=asc")
		Expect(header.Get("X-Total-Count")).To(Equal("10"))
		Expect(column(rows, "seq")).To(Equal([]any{
			float64(11), float64(12), float64(13), float64(14), float64(15),
			float64(16), float64(17), float64(18), float64(19), float64(20),
		}))
	})

	It("catches the index up with rows appended since the last read", func() {
		_, err := recordstore.AppendTyped(context.Background(), server.source, "run-2", "sample_event", sampleEvents(1005, 1006))
		Expect(err).ToNot(HaveOccurred())
		_, header := server.rows("stream=run-2")
		Expect(header.Get("X-Total-Count")).To(Equal("7"))
	})

	It("answers a stream nobody wrote with a 404 rather than an empty page", func() {
		response := server.get(profilePath+"?stream=run-404", "application/json")
		Expect(response.Code).To(Equal(http.StatusNotFound), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring("run-404"))
	})

	It("answers a read without a stream with a 400", func() {
		response := server.get(profilePath, "application/json")
		Expect(response.Code).To(Equal(http.StatusBadRequest), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring("stream"))
	})

	It("answers a stream id no backend could key by with a 400", func() {
		response := server.get(profilePath+"?stream="+url.QueryEscape("run/1"), "application/json")
		Expect(response.Code).To(Equal(http.StatusBadRequest), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring("run/1"))
	})
})

// unreachableStore fails every read the way a valkey that is down does.
type unreachableStore struct{ cache.Store }

func (unreachableStore) Get(context.Context, string) ([]byte, error) {
	return nil, errors.New("dial tcp 10.0.0.1:6379: connection refused")
}

var _ = Describe("a record result type whose source is unreachable", func() {
	It("answers with a 500, since nothing in the request is wrong", func() {
		schemas := recordstore.NewSchemas()
		source, err := kv.New(kv.Options{
			Store: unreachableStore{Store: cache.NewMemory()}, Prefix: "records", Schema: schemas.Kind, TTL: time.Hour,
			MaxChunkBytes: 1 << 20,
		})
		Expect(err).ToNot(HaveOccurred())
		server := resultServer{handler: serveResults(newSampleRegistry(source, schemas))}

		response := server.get(profilePath+"?stream=run-1", "application/json")
		Expect(response.Code).To(Equal(http.StatusInternalServerError), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring("prepare_failed"))
	})
})
