package providers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// conformanceIDs is the dataset every fixture serves, in order. Six rows is
// enough to walk three pages of two and still have a page that is not the last.
var conformanceIDs = []string{"a1", "a2", "a3", "a4", "a5", "a6"}

// pagingFixture is one provider's way of serving conformanceIDs. The contract
// under test belongs to ExecutePages rather than to any backend, so each fixture
// supplies only what differs: the profile, the context it needs, and how to read
// a row's identity.
type pagingFixture struct {
	name    string
	profile func() query.Profile
	ctx     func() context.Context
	key     func(query.Row) string

	// released reports whether exhausting a resumed cursor released whatever
	// backend snapshot kept that cursor valid. Nil for a backend that holds
	// nothing.
	released func() bool
}

func (f pagingFixture) walk(page query.PageRequest) ([]string, []query.Page) {
	GinkgoHelper()
	var ids []string
	var pages []query.Page
	for got, err := range query.ExecutePages(f.ctx(), f.profile(), page) {
		Expect(err).ToNot(HaveOccurred())
		for _, row := range got.Rows {
			ids = append(ids, f.key(row))
		}
		pages = append(pages, got)
		// A walk that never terminates would otherwise hang the suite rather
		// than fail it.
		Expect(len(pages)).To(BeNumerically("<=", len(conformanceIDs)+1))
	}
	return ids, pages
}

// runPagingConformance asserts the paging contract against one provider. The
// point of running it identically against every backend is that a caller reads
// pages through one interface: a guarantee that holds only on SQL is not a
// guarantee.
func runPagingConformance(f pagingFixture) {
	Describe("paging conformance: "+f.name, func() {
		modes := func() query.PagingMode {
			return query.SupportsPaging(f.profile().Provider.Type)
		}

		It("serves exactly the page it was asked for", func() {
			if !modes().Supports(query.PagingOffset) {
				Skip(f.name + " does not serve offset paging")
			}
			ids, _ := f.walk(query.PageRequest{Limit: 2, Offset: 2})
			Expect(ids[:2]).To(Equal(conformanceIDs[2:4]))
		})

		// The property paging exists for: a full walk is the same rows as one
		// unpaged read, in the same order, with nothing seen twice.
		It("walks every row exactly once, in order", func() {
			ids, _ := f.walk(query.PageRequest{Limit: 2})
			Expect(ids).To(Equal(conformanceIDs))
		})

		// A short page and the end of the data are different facts, and a
		// consumer deciding whether to ask again depends on which one it was.
		It("reports HasMore only while rows remain", func() {
			_, pages := f.walk(query.PageRequest{Limit: 2})
			for i, page := range pages[:len(pages)-1] {
				Expect(page.HasMore).To(BeTrue(), "page %d of %d claims to be the last", i, len(pages))
			}
			Expect(pages[len(pages)-1].HasMore).To(BeFalse())
		})

		It("never returns more rows than the page asked for", func() {
			_, pages := f.walk(query.PageRequest{Limit: 2})
			for _, page := range pages {
				Expect(len(page.Rows)).To(BeNumerically("<=", 2))
			}
		})

		It("resumes from the cursor it handed back", func() {
			if !modes().Supports(query.PagingCursor) {
				Skip(f.name + " does not serve cursor paging")
			}
			var first query.Page
			for page, err := range query.ExecutePages(f.ctx(), f.profile(),
				query.PageRequest{Limit: 2, Strategy: query.PagingCursor}) {
				Expect(err).ToNot(HaveOccurred())
				first = page
				break
			}
			Expect(first.Next).ToNot(BeEmpty(), "a page with more to come carries no cursor")

			ids, _ := f.walk(query.PageRequest{Limit: 2, Cursor: first.Next})
			// Resuming after the first page must neither repeat nor skip a row.
			Expect(ids).To(Equal(conformanceIDs[2:]))
			if f.released != nil {
				Expect(f.released()).To(BeTrue(), "the exhausted cursor still holds its backend snapshot")
			}
		})
	})
}

// kubeletStub serves conformanceIDs the way a kubelet does: one line per
// second, oldest first, honouring sinceTime and tailLines and nothing else.
// There is no offset to honour, which is the constraint the k8s cursor exists
// to work around — a stub that served one would test a backend that does not
// exist.
type kubeletStub struct {
	server *httptest.Server
	lines  []string
}

func newKubeletStub() *kubeletStub {
	// Inside the generated time control's one-hour default, so a walk that
	// passes no params still sees every line.
	base := time.Now().Add(-30 * time.Minute).UTC().Truncate(time.Second)
	lines := make([]string, 0, len(conformanceIDs))
	for i, id := range conformanceIDs {
		lines = append(lines, base.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano)+" "+id)
	}
	return newKubeletStubWith(lines)
}

func newKubeletStubWith(lines []string) *kubeletStub {
	stub := &kubeletStub{lines: lines}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pods", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"apiVersion": "v1", "kind": "PodList",
			"items": []any{podJSON("logs", map[string]any{"app": "logs"}, "app")},
		})
	})
	for _, resource := range []string{"deployments", "statefulsets", "daemonsets"} {
		mux.HandleFunc("/apis/apps/v1/"+resource, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{"apiVersion": "apps/v1", "items": []any{}})
		})
	}
	mux.HandleFunc("/api/v1/namespaces/prod/pods/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		for _, line := range serveKubeletLines(r, stub.lines) {
			_, _ = fmt.Fprintln(w, line)
		}
	})
	stub.server = httptest.NewServer(mux)
	return stub
}

func (s *kubeletStub) context() context.Context {
	return context.New().WithDB(connectionsDB(models.Connection{
		ID: uuid.New(), Name: "kube", Type: models.ConnectionTypeKubernetes,
		Certificate: kubeconfigFor(s.server.URL),
	}), nil)
}

// pagingStub serves conformanceIDs the way an index does: honouring from/size,
// search_after and the point-in-time a cursor walk opens. It is a stub of the
// mechanism, not of the answer — a stub that ignored from/size could not fail
// the conformance suite.
type pagingStub struct {
	server     *httptest.Server
	ids        []string
	openedPITs int
	closedPITs int
}

func newPagingStub(ids ...string) *pagingStub {
	if len(ids) == 0 {
		ids = conformanceIDs
	}
	stub := &pagingStub{ids: ids}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(r.URL.Path, "/_search/point_in_time") {
			if r.Method == http.MethodDelete {
				stub.closedPITs++
				_, _ = fmt.Fprint(w, `{"pits":[{"successful":true,"pit_id":"pit-1"}]}`)
				return
			}
			stub.openedPITs++
			_, _ = fmt.Fprint(w, `{"pit_id":"pit-1","creation_time":1}`)
			return
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		size, _ := strconv.Atoi(r.URL.Query().Get("size"))
		if size <= 0 {
			size = len(stub.ids)
		}
		start := 0
		if from, ok := body["from"].(float64); ok {
			start = int(from)
		}
		// search_after carries the sort value of the previous page's last row,
		// which for this dataset is the row's own index.
		if after, ok := body["search_after"].([]any); ok && len(after) > 0 {
			if value, ok := after[0].(float64); ok {
				start = int(value) + 1
			}
		}
		_, _ = fmt.Fprint(w, stub.hits(start, size))
	}))
	return stub
}

func (s *pagingStub) hits(start, size int) string {
	start = min(start, len(s.ids))
	end := min(start+size, len(s.ids))
	rendered := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		rendered = append(rendered, fmt.Sprintf(
			`{"_index":"logs","_id":%q,"_source":{"seq":%d,"message":%q},"sort":[%d]}`,
			s.ids[i], i, s.ids[i], i))
	}
	return fmt.Sprintf(`{"took":1,"timed_out":false,"hits":{"total":{"value":%d,"relation":"eq"},"hits":[%s]}}`,
		len(s.ids), strings.Join(rendered, ","))
}

// osProfile is an ordered OpenSearch profile reading this stub's index.
//
// It orders by message rather than seq because a join compares keys as strings:
// a numeric seq would order "10" before "9" here and after it at the backend,
// which the merge join refuses outright rather than reporting the disagreement
// as missing keys.
func (s *pagingStub) osProfile(name string) query.Profile {
	return query.Profile{
		Name: name,
		Provider: query.ProviderConfig{
			Type:    "opensearch",
			Options: map[string]any{"address": s.server.URL, "index": "logs"},
		},
		Query: `{"query":{"match_all":{}}}`,
		Order: query.Order{{Column: "message", Unique: true}},
	}
}

var _ = Describe("provider paging contract", func() {
	Context("opensearch", func() {
		var stub *pagingStub
		BeforeEach(func() {
			stub = newPagingStub()
			DeferCleanup(stub.server.Close)
		})

		runPagingConformance(pagingFixture{
			name: "opensearch",
			ctx:  func() context.Context { return context.New() },
			key:  func(row query.Row) string { return fmt.Sprint(row["message"]) },
			profile: func() query.Profile {
				return query.Profile{
					Name: "conformance-opensearch",
					Provider: query.ProviderConfig{
						Type:    "opensearch",
						Options: map[string]any{"address": stub.server.URL, "index": "logs"},
					},
					Query: `{"query":{"match_all":{}}}`,
					Order: query.Order{{Column: "seq", Unique: true}},
				}
			},
			// The point-in-time is the resource that keeps a returned cursor
			// resumable, so it is released only when that cursor is exhausted.
			released: func() bool { return stub.closedPITs == stub.openedPITs && stub.openedPITs > 0 },
		})
	})

	Context("opentelemetry", func() {
		var stub *pagingStub
		BeforeEach(func() {
			stub = newPagingStub()
			DeferCleanup(stub.server.Close)
		})

		runPagingConformance(pagingFixture{
			name: "opentelemetry",
			ctx:  func() context.Context { return context.New().WithDB(traceConnections(stub.server.URL), nil) },
			key:  func(row query.Row) string { return fmt.Sprint(row["message"]) },
			profile: func() query.Profile {
				return query.Profile{
					Name: "conformance-opentelemetry",
					Provider: query.ProviderConfig{
						Type: "opentelemetry", Connection: "connection://traces",
						Options: map[string]any{
							"format": "flat", "index": "logs", "dateField": "seq",
							"selectFields": []string{"message", "seq"},
						},
					},
					Order: query.Order{{Column: "seq", Unique: true}},
				}
			},
			released: func() bool { return stub.closedPITs == stub.openedPITs && stub.openedPITs > 0 },
		})
	})

	// The Kubernetes log API has no offset and no continuation token, so its
	// cursor is synthesised from SinceTime. Holding it to the same contract as
	// the backends that page natively is the only way to know the synthesis is
	// sound rather than merely plausible.
	Context("k8s", func() {
		var stub *kubeletStub
		BeforeEach(func() {
			stub = newKubeletStub()
			DeferCleanup(stub.server.Close)
		})

		runPagingConformance(pagingFixture{
			name: "k8s",
			ctx:  func() context.Context { return stub.context() },
			key:  func(row query.Row) string { return fmt.Sprint(row["message"]) },
			profile: func() query.Profile {
				return query.Profile{
					Name: "conformance-k8s",
					Provider: query.ProviderConfig{
						Type: "k8s", Connection: "connection://kube",
					},
					Query: "kind=Pod namespace=prod name=logs",
					Order: query.Order{
						{Column: "timestamp"},
						{Column: "id", Unique: true},
					},
				}
			},
		})
	})

	// SQL cursor predicates are compiled by the provider around the author's
	// statement, so values are bound and every ordered profile gets the same
	// paging semantics without writing transport logic into its query.
	Context("sql", Ordered, func() {
		var dsn string
		BeforeAll(func() {
			dsn = dbtest.ForGinkgo(dbtest.Options{Name: "paging_conformance"}).DSN()
		})

		runPagingConformance(pagingFixture{
			name: "sql",
			ctx:  func() context.Context { return context.New() },
			key:  func(row query.Row) string { return fmt.Sprint(row["message"]) },
			profile: func() query.Profile {
				values := make([]string, 0, len(conformanceIDs))
				for i, id := range conformanceIDs {
					values = append(values, fmt.Sprintf("(%d, '%s')", i, id))
				}
				return query.Profile{
					Name: "conformance-sql",
					Provider: query.ProviderConfig{
						Type:    "sql",
						Options: map[string]any{"type": "postgres", "url": dsn},
					},
					Query: fmt.Sprintf(
						`select seq, message from (values %s) as t(seq, message)`,
						strings.Join(values, ", ")),
					Order: query.Order{{Column: "seq", Unique: true}},
				}
			},
		})
	})
})

// The reconcile P0: two OpenSearch sides used to be read to a 500-row backend
// default each, and every key past that came back as a one-sided finding. The
// findings were the bound, not the data. A merge join walks both sides in key
// order to the end, so a run over more rows than any single search returns has
// to agree with the datasets themselves.
var _ = Describe("opensearch reconcile past the old backend default", func() {
	ids := func(count int) []string {
		out := make([]string, count)
		for i := range out {
			out[i] = fmt.Sprintf("ord-%05d", i)
		}
		return out
	}

	It("matches every key on both sides rather than stopping at 500", func() {
		const rows = 1500
		source := newPagingStub(ids(rows)...)
		defer source.server.Close()
		dest := newPagingStub(ids(rows)...)
		defer dest.server.Close()

		result, err := query.ReconcileProfiles(context.New(), query.ReconcileRun{
			Source: source.osProfile("orders-emitted"),
			Dest:   dest.osProfile("orders-ingested"),
			Config: query.ReconcileConfig{
				Dest:          "orders-ingested",
				ReconcileSpec: query.ReconcileSpec{Key: query.KeySpec{Columns: []string{"message"}}},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Mode).To(Equal(query.ReconcileMerged))
		Expect(result.Stats).To(Equal(query.ReconcileStats{Matched: rows}))
		Expect(result.Bounded()).To(BeFalse())
	})

	// The other half of the same guarantee: a key genuinely missing downstream
	// is still reported, so bounding the read correctly has not blunted it.
	It("still finds a key the destination is missing", func() {
		source := newPagingStub(ids(1200)...)
		defer source.server.Close()
		dest := newPagingStub(ids(1199)...)
		defer dest.server.Close()

		result, err := query.ReconcileProfiles(context.New(), query.ReconcileRun{
			Source: source.osProfile("orders-emitted"),
			Dest:   dest.osProfile("orders-ingested"),
			Config: query.ReconcileConfig{
				Dest:          "orders-ingested",
				ReconcileSpec: query.ReconcileSpec{Key: query.KeySpec{Columns: []string{"message"}}},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Stats).To(Equal(query.ReconcileStats{Matched: 1199, OnlySource: 1}))
	})
})
