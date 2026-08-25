package profiles

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
)

var _ = Describe("profile OpenAPI requests", func() {
	It("serializes generation and extension of the shared OpenAPI document", func() {
		store, err := NewFileStore(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())

		var active atomic.Int32
		var peak atomic.Int32
		extension := func(*rpc.OpenAPISpec) {
			current := active.Add(1)
			defer active.Add(-1)
			for previous := peak.Load(); current > previous; previous = peak.Load() {
				if peak.CompareAndSwap(previous, current) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
		}

		handler := newProfileOpenAPIHandler(
			&cobra.Command{Use: "query"},
			&rpc.Config{},
			store,
			[]OpenAPIExtension{extension},
		)
		const requestCount = 8
		start := make(chan struct{})
		responses := make(chan *httptest.ResponseRecorder, requestCount)
		var requests sync.WaitGroup
		requests.Add(requestCount)
		for range requestCount {
			go func() {
				defer GinkgoRecover()
				defer requests.Done()
				<-start
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/openapi.json", nil))
				responses <- response
			}()
		}
		close(start)
		requests.Wait()
		close(responses)

		for response := range responses {
			Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		}
		Expect(peak.Load()).To(Equal(int32(1)))
	})
})
