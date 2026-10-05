// Specs for the HTTP traffic tap: collectors observing a feature receive every
// exchange its transports make, regardless of the feature's HAR level.

package connection

import (
	"context"
	"io"
	netHTTP "net/http"
	"net/http/httptest"
	"sync"

	"github.com/flanksource/commons/har"
	commonsHTTP "github.com/flanksource/commons/http"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("HTTP traffic tap", func() {
	var server *httptest.Server

	BeforeEach(func() {
		server = httptest.NewServer(netHTTP.HandlerFunc(func(w netHTTP.ResponseWriter, _ *netHTTP.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
		DeferCleanup(server.Close)
	})

	get := func(feature string) {
		client := &netHTTP.Client{Transport: ApplyHTTPObservability(context.Background(), feature, nil, nil)}
		resp, err := client.Get(server.URL + "/api?page=2")
		Expect(err).ToNot(HaveOccurred())
		_, _ = io.Copy(io.Discard, resp.Body)
		Expect(resp.Body.Close()).To(Succeed())
	}

	It("hands every exchange of an observed feature to its collector, though HAR capture is off", func() {
		collector := har.NewCollector(har.DefaultConfig())
		release := ObserveHTTP("prometheus", collector)
		DeferCleanup(release)

		get("prometheus")
		entries := collector.Entries()
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Request.Method).To(Equal("GET"))
		Expect(entries[0].Request.URL).To(Equal(server.URL + "/api?page=2"))
		Expect(entries[0].Response.Status).To(Equal(200))
		Expect(entries[0].Response.Content.Text).To(Equal(`{"ok":true}`))
	})

	It("keeps features apart, matching names as the HAR level does", func() {
		prometheus, loki := har.NewCollector(har.DefaultConfig()), har.NewCollector(har.DefaultConfig())
		DeferCleanup(ObserveHTTP(" Prometheus ", prometheus))
		DeferCleanup(ObserveHTTP("loki", loki))

		get("prometheus")
		Expect(prometheus.Entries()).To(HaveLen(1))
		Expect(loki.Entries()).To(BeEmpty())
	})

	It("hands one exchange to every collector observing its feature", func() {
		first, second := har.NewCollector(har.DefaultConfig()), har.NewCollector(har.DefaultConfig())
		DeferCleanup(ObserveHTTP("prometheus", first))
		DeferCleanup(ObserveHTTP("prometheus", second))

		get("prometheus")
		Expect(first.Entries()).To(HaveLen(1))
		Expect(second.Entries()).To(HaveLen(1))
	})

	It("stops once released, and a second release is harmless", func() {
		collector := har.NewCollector(har.DefaultConfig())
		release := ObserveHTTP("prometheus", collector)
		release()
		release()

		get("prometheus")
		Expect(collector.Entries()).To(BeEmpty())
	})

	It("observes a commons HTTP client's requests", func() {
		collector := har.NewCollector(har.DefaultConfig())
		DeferCleanup(ObserveHTTP("http", collector))

		client := commonsHTTP.NewClient()
		ApplyHTTPClientObservability(context.Background(), "http", client, nil)
		resp, err := client.R(context.Background()).Get(server.URL + "/client")
		Expect(err).ToNot(HaveOccurred())
		_, err = resp.AsString()
		Expect(err).ToNot(HaveOccurred())

		Expect(collector.Entries()).To(HaveLen(1))
		Expect(collector.Entries()[0].Request.URL).To(Equal(server.URL + "/client"))
	})

	It("lets collectors come and go while requests are in flight", func() {
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(2)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				release := ObserveHTTP("prometheus", har.NewCollector(har.DefaultConfig()))
				release()
			}()
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				get("prometheus")
			}()
		}
		wg.Wait()
	})
})
