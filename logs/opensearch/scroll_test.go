package opensearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestScrollTransport(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "OpenSearch scroll transport")
}

var _ = Describe("scroll transport", func() {
	DescribeTable("sends opaque IDs in JSON bodies without growing the request URL", func(scrollID string) {
		var methods []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			if r.Method == http.MethodHead {
				return
			}
			methods = append(methods, r.Method)
			Expect(r.URL.Path).To(Equal("/_search/scroll"))
			Expect(r.URL.Query()).NotTo(HaveKey("scroll_id"))
			Expect(r.Header.Get("Content-Type")).To(Equal("application/json"))
			var body map[string]any
			Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
			if r.Method == http.MethodDelete {
				Expect(body).To(Equal(map[string]any{"scroll_id": []any{scrollID}}))
				_, _ = fmt.Fprint(w, `{"succeeded":true,"num_freed":1}`)
				return
			}
			Expect(r.URL.Query().Get("scroll")).To(Equal("120000ms"))
			Expect(body).To(Equal(map[string]any{"scroll_id": scrollID}))
			_, _ = fmt.Fprint(w, `{"_scroll_id":"next","hits":{"hits":[]}}`)
		}))
		defer server.Close()
		ctx := context.New()
		searcher, err := New(ctx, Backend{Address: server.URL}, nil)
		Expect(err).NotTo(HaveOccurred())
		response, err := searcher.ScrollNext(ctx, ScrollPageRequest{ID: scrollID, KeepAlive: 2 * time.Minute})
		Expect(err).NotTo(HaveOccurred())
		Expect(response.ScrollID).To(Equal("next"))
		Expect(searcher.ClearScroll(ctx, scrollID)).To(Succeed())
		Expect(methods).To(Equal([]string{http.MethodPost, http.MethodDelete}))
	},
		Entry("long IDs", strings.Repeat("opaque-scroll-id", 1024)),
		Entry("escaped characters", "opaque/+=\"\\\nidentifier"),
	)
})
