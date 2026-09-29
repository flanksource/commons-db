package recordresultse2e

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"
)

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
		settings := recordresultstest.LocalSettings(recordstore.BackendSQLite)
		results := recordresultstest.OpenResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: settings, Register: recordresultstest.RegisterSampleEvents,
		})
		_, err := recordstore.AppendTyped(ctx, results.Backend, "run-1", "sample_event", recordresultstest.SampleEvents(1, 30))
		Expect(err).ToNot(HaveOccurred())

		response := getFor(serveResults(results.Registry), ctx, "stream=run-1")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Header().Get("X-Total-Count")).To(Equal("30"))
		Expect(filepath.Join(settings.Dir, "v6", "records.sqlite")).To(BeAnExistingFile())
		Expect(filepath.Join(settings.Dir, "v6", "index.sqlite")).ToNot(BeAnExistingFile())
	})

	It("opens local ndjson streams mirrored into a derived index", func() {
		settings := recordresultstest.LocalSettings(recordstore.BackendNDJSON)
		results := recordresultstest.OpenResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: settings, Register: recordresultstest.RegisterSampleEvents,
		})
		_, err := recordstore.AppendTyped(ctx, results.Backend, "run-1", "sample_event", recordresultstest.SampleEvents(1, 12))
		Expect(err).ToNot(HaveOccurred())

		response := getFor(serveResults(results.Registry), ctx, "stream=run-1")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Header().Get("X-Total-Count")).To(Equal("12"))
		Expect(filepath.Join(settings.Dir, "v6", "index.sqlite")).To(BeAnExistingFile())
	})

	It("deletes one stream from its source and derived index only when its kind matches", func() {
		settings := recordresultstest.LocalSettings(recordstore.BackendNDJSON)
		results := recordresultstest.OpenResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: settings, Register: recordresultstest.RegisterSampleEvents,
		})
		for _, stream := range []string{"old-run", "keep-run"} {
			_, err := recordstore.AppendTyped(ctx, results.Backend, stream, "sample_event", recordresultstest.SampleEvents(1, 2))
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
		index, err := sql.Open("sqlite", filepath.Join(settings.Dir, "v6", "index.sqlite"))
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(index.Close)
		var remaining int
		Expect(index.QueryRow(`SELECT COUNT(*) FROM record_streams WHERE stream_id = 'old-run'`).Scan(&remaining)).To(Succeed())
		Expect(remaining).To(BeZero())
	})

	It("keeps a routed source's streams to the tenant that wrote them", func() {
		schemas := recordstore.NewSchemas()
		router := recordresultstest.KVRouter(schemas)
		settings := recordresultstest.LocalSettings("")
		results := recordresultstest.OpenResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: settings, Source: router,
			Schemas: schemas, Register: recordresultstest.RegisterSampleEvents,
		})
		_, err := recordstore.AppendTyped(recordresultstest.ForTenant("a"), results.Backend, "run-1", "sample_event", recordresultstest.SampleEvents(1, 7))
		Expect(err).ToNot(HaveOccurred())
		handler := serveResults(results.Registry)

		By("serving the stream to the tenant that wrote it")
		response := getFor(handler, recordresultstest.ForTenant("a"), "stream=run-1")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Header().Get("X-Total-Count")).To(Equal("7"))

		By("answering another tenant as if the stream did not exist, although the index now holds it")
		response = getFor(handler, recordresultstest.ForTenant("b"), "stream=run-1")
		Expect(response.Code).To(Equal(http.StatusNotFound), response.Body.String())

		By("never keeping a durable file a route could share: only the derived index")
		Expect(filepath.Join(settings.Dir, "v6", "index.sqlite")).To(BeAnExistingFile())
		Expect(filepath.Join(settings.Dir, "v6", "records.sqlite")).ToNot(BeAnExistingFile())
	})
})
