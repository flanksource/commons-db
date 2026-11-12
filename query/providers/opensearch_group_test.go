package providers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OpenSearch wildcard scroll", func() {
	It("preserves the wildcard and limit and clears the latest returned scroll ID", func() {
		var paths []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			if r.Method == http.MethodHead {
				return
			}
			paths = append(paths, r.URL.Path)
			switch r.Method {
			case http.MethodDelete:
				var body map[string][]string
				Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
				Expect(body["scroll_id"]).To(Equal([]string{"latest-scroll"}))
				_, _ = fmt.Fprint(w, `{"succeeded":true,"num_freed":1}`)
			case http.MethodPost:
				if r.URL.Path == "/_search/scroll" {
					var body map[string]string
					Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
					Expect(body["scroll_id"]).To(Equal("initial-scroll"))
					_, _ = fmt.Fprint(w, `{"_scroll_id":"latest-scroll","hits":{"hits":[]}}`)
					return
				}
				Expect(r.URL.Path).To(Equal("/logs-*/_search"))
				Expect(r.URL.Query().Get("size")).To(Equal("100"))
				Expect(r.URL.Query().Get("scroll")).NotTo(BeEmpty())
				hits := make([]map[string]any, 100)
				for i := range hits {
					hits[i] = map[string]any{"_id": fmt.Sprint(i), "_source": map[string]any{"message": "entry"}, "sort": []any{i}}
				}
				Expect(json.NewEncoder(w).Encode(map[string]any{"_scroll_id": "initial-scroll", "hits": map[string]any{"hits": hits}})).To(Succeed())
			default:
				http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			}
		}))
		defer server.Close()
		profile := query.Profile{Name: "wildcard", Provider: query.ProviderConfig{Type: "opensearch", Options: map[string]any{"address": server.URL, "index": "logs-*", "pagingThreshold": 0}}, Query: `{"query":{"match_all":{}}}`, Order: query.Order{{Column: "message", Unique: true}}}
		var rows int
		for page, err := range query.ExecutePages(context.New(), profile, query.PageRequest{Limit: 100, Strategy: query.PagingCursor}) {
			Expect(err).NotTo(HaveOccurred())
			rows += len(page.Rows)
		}
		Expect(rows).To(Equal(100))
		Expect(paths).To(Equal([]string{"/logs-*/_search", "/_search/scroll", "/_search/scroll"}))
	})
})
