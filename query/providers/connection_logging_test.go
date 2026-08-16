package providers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"

	clickytext "github.com/flanksource/clicky/text"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	commons "github.com/flanksource/commons/context"
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Connection logging through HTTP providers", func() {
	It("emits cumulative sanitized HTTP details at trace2", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Authorization", "Bearer response-token")
			_, _ = w.Write([]byte(`{"token":"hunter2","id":7}`))
		}))
		defer server.Close()

		buffer := logger.NewBufferedLogger(20)
		buffer.SetLogLevel(logger.Trace2)
		ctx := dbcontext.New(commons.WithLogger(buffer))

		result, err := query.Execute(ctx, query.Profile{
			Name: "http logging",
			Provider: query.ProviderConfig{
				Type:    "http",
				Options: map[string]any{"url": server.URL},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows).To(Equal([]query.Row{{"token": "hunter2", "id": float64(7)}}))

		messages := make([]string, len(buffer.GetLogs()))
		for index, entry := range buffer.GetLogs() {
			messages[index] = entry.Message
		}
		joined := clickytext.StripANSI(strings.Join(messages, "\n"))
		Expect(joined).To(And(
			ContainSubstring("[http/inline]"),
			ContainSubstring("GET "+server.URL),
			ContainSubstring("200 OK"),
			ContainSubstring("Request Headers"),
			ContainSubstring("Response Headers"),
			ContainSubstring("Response Body"),
			ContainSubstring(`"token": "h****"`),
			Not(ContainSubstring("hunter2")),
			Not(ContainSubstring("response-token")),
		))
	})
})
