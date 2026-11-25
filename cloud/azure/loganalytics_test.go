package azure_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cloud/azure"
)

func ctx() context.Context { return context.Background() }

func errorsAs(err error, target any) bool { return errors.As(err, target) }

var _ = Describe("LogAnalyticsClient", func() {
	var aad *fakeAAD

	BeforeEach(func() { aad = newFakeAAD() })
	AfterEach(func() { aad.Close() })

	const body = `{"tables":[
	  {"name":"PrimaryResult","columns":[{"name":"TimeGenerated","type":"datetime"},{"name":"Count","type":"long"},{"name":"Ok","type":"bool"},{"name":"Msg","type":"string"}],
	   "rows":[["2024-01-01T00:00:00Z",3,true,"hello"],["2024-01-02T00:00:00Z",null,false,null]]},
	  {"name":"Second","columns":[{"name":"X","type":"real"}],"rows":[[1.5]]}]}`

	It("posts the query with endpoint and scope overrides and decodes tables, types and nulls", func() {
		var got recorded
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = record(r)
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()
		c, err := azure.NewLogAnalyticsClient(azure.LogAnalyticsOptions{Credential: aad.Credential(), Endpoint: srv.URL + "/v1", Scope: "https://api.loganalytics.us/.default"})
		Expect(err).ToNot(HaveOccurred())

		res, err := c.Query(ctx(), azure.QueryRequest{WorkspaceID: "ws-1", Query: "AzureActivity | take 2", Timespan: "2024-01-01T00:00:00Z/2024-01-03T00:00:00Z"})
		Expect(err).ToNot(HaveOccurred())

		Expect(got).To(Equal(recorded{
			Method: "POST", Path: "/v1/workspaces/ws-1/query", Query: url.Values{}, Auth: "Bearer " + fakeAccessToken,
			Body: map[string]any{"query": "AzureActivity | take 2", "timespan": "2024-01-01T00:00:00Z/2024-01-03T00:00:00Z"},
		}))
		Expect(aad.Scopes()).To(Equal([]string{"https://api.loganalytics.us/.default"}))
		Expect(res.Tables).To(Equal([]azure.Table{
			{Name: "PrimaryResult", Columns: []azure.Column{{Name: "TimeGenerated", Type: "datetime"}, {Name: "Count", Type: "long"}, {Name: "Ok", Type: "bool"}, {Name: "Msg", Type: "string"}},
				Rows: [][]any{{"2024-01-01T00:00:00Z", float64(3), true, "hello"}, {"2024-01-02T00:00:00Z", nil, false, nil}}},
			{Name: "Second", Columns: []azure.Column{{Name: "X", Type: "real"}}, Rows: [][]any{{1.5}}},
		}))
	})

	It("omits timespan when none is given and uses the public scope by default", func() {
		var got recorded
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = record(r)
			_, _ = w.Write([]byte(`{"tables":[]}`))
		}))
		defer srv.Close()
		c, err := azure.NewLogAnalyticsClient(azure.LogAnalyticsOptions{Credential: aad.Credential(), Endpoint: srv.URL})
		Expect(err).ToNot(HaveOccurred())
		_, err = c.Query(ctx(), azure.QueryRequest{WorkspaceID: "ws", Query: "q"})
		Expect(err).ToNot(HaveOccurred())
		Expect(got.Body).To(Equal(map[string]any{"query": "q"}))
		Expect(aad.Scopes()).To(Equal([]string{azure.DefaultLogAnalyticsScope}))
	})

	It("surfaces the partial-failure error in the result and APIError on HTTP failure", func() {
		status := 200
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"tables":[],"error":{"code":"PartialError","message":"boom"}}`))
		}))
		defer srv.Close()
		c, err := azure.NewLogAnalyticsClient(azure.LogAnalyticsOptions{Credential: aad.Credential(), Endpoint: srv.URL})
		Expect(err).ToNot(HaveOccurred())

		res, err := c.Query(ctx(), azure.QueryRequest{WorkspaceID: "ws", Query: "q"})
		Expect(err).ToNot(HaveOccurred())
		Expect(res.Error).To(Equal(&azure.ErrorDetail{Code: "PartialError", Message: "boom"}))

		status = 403
		_, err = c.Query(ctx(), azure.QueryRequest{WorkspaceID: "ws", Query: "q"})
		var apiErr *azure.APIError
		Expect(errorsAs(err, &apiErr)).To(BeTrue())
		Expect(apiErr.StatusCode).To(Equal(403))
	})

	It("requires workspace and query", func() {
		c, err := azure.NewLogAnalyticsClient(azure.LogAnalyticsOptions{Credential: aad.Credential()})
		Expect(err).ToNot(HaveOccurred())
		_, err = c.Query(ctx(), azure.QueryRequest{Query: "q"})
		Expect(err).To(MatchError(ContainSubstring("workspaceID")))
	})
})
