package sse_test

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/internal/sse"
)

func TestSSE(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SSE Suite")
}

var _ = Describe("Begin", func() {
	It("lifts the server's write timeout, so a stream outlives it", func() {
		const writeTimeout = 100 * time.Millisecond
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			defer GinkgoRecover()
			flusher, err := sse.Begin(w)
			Expect(err).ToNot(HaveOccurred())
			for index := int64(1); index <= 3; index++ {
				time.Sleep(writeTimeout)
				Expect(sse.WriteFrame(w, sse.Frame{Event: "event", ID: index, Data: index})).To(Succeed())
				flusher.Flush()
			}
		}))
		server.Config.WriteTimeout = writeTimeout
		server.Start()
		DeferCleanup(server.Close)

		response, err := http.Get(server.URL)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(response.Body.Close)
		var ids []string
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			if id, ok := strings.CutPrefix(scanner.Text(), "id: "); ok {
				ids = append(ids, id)
			}
		}
		Expect(scanner.Err()).ToNot(HaveOccurred())
		Expect(ids).To(Equal([]string{"1", "2", "3"}))
	})

	It("streams through a writer that cannot set deadlines", func() {
		recorder := httptest.NewRecorder()
		flusher, err := sse.Begin(recorder)
		Expect(err).ToNot(HaveOccurred())
		flusher.Flush()
		Expect(recorder.Header().Get("Content-Type")).To(Equal("text/event-stream"))
	})
})
