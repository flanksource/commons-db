package query_test

import (
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ProviderDiagnostics HTTPTransport", func() {
	var server *httptest.Server
	var paths []string

	BeforeEach(func() {
		paths = nil
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Set-Cookie", "session=hunter2")
			w.WriteHeader(http.StatusTeapot)
		}))
	})

	AfterEach(func() { server.Close() })

	call := func(client *http.Client, path string, header http.Header) {
		request, err := http.NewRequest(http.MethodPost, server.URL+path, nil)
		Expect(err).ToNot(HaveOccurred())
		request.Header = header
		response, err := client.Do(request)
		Expect(err).ToNot(HaveOccurred())
		Expect(response.Body.Close()).To(Succeed())
	}

	It("records the exchange carrying the request the provider described, credentials masked", func() {
		diagnostics := query.NewProviderDiagnostics("opensearch", "", nil)
		client := &http.Client{Transport: diagnostics.HTTPTransport(nil)}

		// A client pings before it searches, and a ping the provider never
		// described is not the request anyone opened the dialog to read.
		call(client, "/", http.Header{})
		diagnostics.RecordRequest(`{"query":{"match_all":{}}}`, nil, nil)
		call(client, "/logs-2026/_search", http.Header{
			"Content-Type":  []string{"application/json"},
			"Authorization": []string{"Basic c2VjcmV0"},
		})
		call(client, "/_search/point_in_time", http.Header{})

		snapshot := diagnostics.Snapshot()
		Expect(paths).To(HaveLen(3))
		Expect(snapshot.Request.Method).To(Equal(http.MethodPost))
		Expect(snapshot.Request.URL).To(Equal(server.URL + "/logs-2026/_search"))
		Expect(snapshot.Request.Headers).To(Equal(map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "********",
		}))
		Expect(snapshot.Response.Status).To(Equal(http.StatusTeapot))
		Expect(snapshot.Response.Headers).To(HaveKeyWithValue("Set-Cookie", "********"))
		Expect(snapshot.Response.Headers).To(HaveKeyWithValue("Content-Type", "application/json"))
	})

	It("keeps a walk's first page rather than its last", func() {
		diagnostics := query.NewWalkDiagnostics("opensearch")
		client := &http.Client{Transport: diagnostics.HTTPTransport(nil)}

		diagnostics.RecordRequest(`{"from":0}`, nil, nil)
		call(client, "/logs-2026/_search", http.Header{})
		diagnostics.RecordRequest(`{"from":1000}`, nil, nil)
		call(client, "/logs-2026/_search?page=2", http.Header{})

		snapshot := diagnostics.Snapshot()
		Expect(snapshot.Request.Query).To(Equal(`{"from":0}`))
		Expect(snapshot.Request.URL).To(Equal(server.URL + "/logs-2026/_search"))
	})
})
