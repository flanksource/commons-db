package recordresults_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/recordstore"
)

// tenantKey carries the tenant a request runs for, the way a server's
// middleware puts it on the request context.
type tenantKey struct{}

func forTenant(tenant string) context.Context {
	return context.WithValue(context.Background(), tenantKey{}, tenant)
}

func tenantOf(ctx context.Context) (string, error) {
	tenant, _ := ctx.Value(tenantKey{}).(string)
	if tenant == "" {
		return "", errors.New("the request carries no tenant")
	}
	return tenant, nil
}

func registerSampleEvents(registry *recordresults.Registry) error {
	return recordresults.RegisterResultType(registry, recordresults.ResultType[sampleEvent]{
		Kind: "sample_event", Title: "Sample events", TimeColumn: "at",
	})
}

// kvRouter routes each tenant to an in-process kv store of its own, resolving
// kinds through schemas.
func kvRouter(schemas *recordstore.Schemas) *recordstore.Router {
	router, err := recordstore.NewRouter(recordstore.RouterOptions{
		Route: tenantOf,
		Open: func(context.Context, string) (recordstore.Backend, error) {
			return newKV(schemas), nil
		},
	})
	Expect(err).ToNot(HaveOccurred())
	return router
}

func localSettings(backend recordstore.BackendKind) recordstore.Settings {
	return recordstore.Settings{
		Prefix: "spec", Backend: backend, Dir: GinkgoT().TempDir(), TTL: time.Hour,
		NDJSONMaxBytes: 1 << 20, NDJSONKeepStreams: 5,
	}
}

func openResults(options recordresults.OpenOptions) *recordresults.Results {
	results, err := recordresults.Open(options)
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(results.Close)
	return results
}

// getFor requests a result page as tenant would.
func getFor(handler http.Handler, ctx context.Context, query string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, profilePath+"?"+query, nil).WithContext(ctx)
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

var _ = Describe("Open", func() {
	ctx := context.Background()

	It("opens a local sqlite file that is its own index", func() {
		settings := localSettings(recordstore.BackendSQLite)
		results := openResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: settings, Register: registerSampleEvents,
		})
		_, err := recordstore.AppendTyped(ctx, results.Backend, "run-1", "sample_event", sampleEvents(1, 30))
		Expect(err).ToNot(HaveOccurred())

		response := getFor(serveResults(results.Registry), ctx, "stream=run-1")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Header().Get("X-Total-Count")).To(Equal("30"))
		Expect(filepath.Join(settings.Dir, "v4", "records.sqlite")).To(BeAnExistingFile())
		Expect(filepath.Join(settings.Dir, "v4", "index.sqlite")).ToNot(BeAnExistingFile())
	})

	It("opens local ndjson streams mirrored into a derived index", func() {
		settings := localSettings(recordstore.BackendNDJSON)
		results := openResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: settings, Register: registerSampleEvents,
		})
		_, err := recordstore.AppendTyped(ctx, results.Backend, "run-1", "sample_event", sampleEvents(1, 12))
		Expect(err).ToNot(HaveOccurred())

		response := getFor(serveResults(results.Registry), ctx, "stream=run-1")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Header().Get("X-Total-Count")).To(Equal("12"))
		Expect(filepath.Join(settings.Dir, "v4", "index.sqlite")).To(BeAnExistingFile())
	})

	It("deletes one stream from its source and derived index only when its kind matches", func() {
		settings := localSettings(recordstore.BackendNDJSON)
		results := openResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: settings, Register: registerSampleEvents,
		})
		for _, stream := range []string{"old-run", "keep-run"} {
			_, err := recordstore.AppendTyped(ctx, results.Backend, stream, "sample_event", sampleEvents(1, 2))
			Expect(err).ToNot(HaveOccurred())
			Expect(getFor(serveResults(results.Registry), ctx, "stream="+stream).Code).To(Equal(http.StatusOK))
		}
		Expect(results.DeleteStream(ctx, "old-run", "other_kind")).To(MatchError(ContainSubstring("holds kind")))
		Expect(results.DeleteStream(ctx, "old-run", "sample_event")).To(Succeed())

		_, err := results.Backend.Meta(ctx, "old-run")
		Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue(), "%v", err)
		kept, err := results.Backend.Meta(ctx, "keep-run")
		Expect(err).ToNot(HaveOccurred())
		Expect(kept.Total).To(Equal(int64(2)))
		index, err := sql.Open("sqlite", filepath.Join(settings.Dir, "v4", "index.sqlite"))
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(index.Close)
		var remaining int
		Expect(index.QueryRow(`SELECT COUNT(*) FROM record_streams WHERE stream_id = 'old-run'`).Scan(&remaining)).To(Succeed())
		Expect(remaining).To(BeZero())
	})

	It("keeps a routed source's streams to the tenant that wrote them", func() {
		schemas := recordstore.NewSchemas()
		router := kvRouter(schemas)
		settings := localSettings("")
		results := openResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: settings, Source: router,
			Schemas: schemas, Register: registerSampleEvents,
		})
		_, err := recordstore.AppendTyped(forTenant("a"), results.Backend, "run-1", "sample_event", sampleEvents(1, 7))
		Expect(err).ToNot(HaveOccurred())
		handler := serveResults(results.Registry)

		By("serving the stream to the tenant that wrote it")
		response := getFor(handler, forTenant("a"), "stream=run-1")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Header().Get("X-Total-Count")).To(Equal("7"))

		By("answering another tenant as if the stream did not exist, although the index now holds it")
		response = getFor(handler, forTenant("b"), "stream=run-1")
		Expect(response.Code).To(Equal(http.StatusNotFound), response.Body.String())

		By("never keeping a durable file a route could share: only the derived index")
		Expect(filepath.Join(settings.Dir, "v4", "index.sqlite")).To(BeAnExistingFile())
		Expect(filepath.Join(settings.Dir, "v4", "records.sqlite")).ToNot(BeAnExistingFile())
	})

	It("closes what it opened, the caller's source included", func() {
		router := kvRouter(recordstore.NewSchemas())
		results, err := recordresults.Open(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: localSettings(""), Source: router,
			Register: registerSampleEvents,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(results.Close()).To(Succeed())
		_, err = results.Backend.Meta(forTenant("a"), "run-1")
		Expect(err).To(MatchError(ContainSubstring("closed")))
	})

	DescribeTable("refuses options it cannot open rather than guessing",
		func(mutate func(*recordresults.OpenOptions), message string) {
			options := recordresults.OpenOptions{
				Prefix: "trace-results", ConnectionName: "index", Settings: localSettings(recordstore.BackendSQLite),
				Register: registerSampleEvents,
			}
			mutate(&options)
			_, err := recordresults.Open(options)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a kv backend with no source to route it", func(o *recordresults.OpenOptions) {
			o.Settings.Backend = recordstore.BackendKV
		}, "Router"),
		Entry("an unresolved backend with no source", func(o *recordresults.OpenOptions) {
			o.Settings.Backend = ""
		}, "Resolve"),
		Entry("no directory", func(o *recordresults.OpenOptions) { o.Settings.Dir = "" }, "directory"),
		Entry("no ttl", func(o *recordresults.OpenOptions) { o.Settings.TTL = 0 }, "ttl"),
		Entry("no result types", func(o *recordresults.OpenOptions) { o.Register = nil }, "Register"),
		Entry("a failing registration", func(o *recordresults.OpenOptions) {
			o.Register = func(*recordresults.Registry) error { return errors.New("bad result type") }
		}, "bad result type"),
	)

	It("closes the caller's source when it fails to open", func() {
		router := kvRouter(recordstore.NewSchemas())
		_, err := recordresults.Open(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: localSettings(""), Source: router,
			Register: func(*recordresults.Registry) error { return errors.New("bad result type") },
		})
		Expect(err).To(HaveOccurred())
		_, err = router.Meta(forTenant("a"), "run-1")
		Expect(err).To(MatchError(ContainSubstring("closed")))
	})

	It("declares result types into the caller's schemas, so a store the caller opens can hold them", func() {
		schemas := recordstore.NewSchemas()
		openResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: localSettings(recordstore.BackendSQLite),
			Schemas: schemas, Register: registerSampleEvents,
		})
		schema, err := schemas.Kind("sample_event")
		Expect(err).ToNot(HaveOccurred())
		Expect(schema.Columns).To(ContainElement(HaveField("Name", "db")))
	})

	It("creates the directory the files go in", func() {
		settings := localSettings(recordstore.BackendSQLite)
		settings.Dir = filepath.Join(settings.Dir, "not", "yet")
		openResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: settings, Register: registerSampleEvents,
		})
		info, err := os.Stat(settings.Dir)
		Expect(err).ToNot(HaveOccurred())
		Expect(info.IsDir()).To(BeTrue())
	})
})
