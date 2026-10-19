package sessions

import (
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/commons-db/tracing"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Trace attachment", func() {
	ginkgo.DescribeTable("keeps untrusted export bytes in a non-executable download", func(format tracing.ExportFormat, contentType string) {
		payload := []byte(`<!doctype html><script>window.injected = true</script>`)
		response := httptest.NewRecorder()
		download := &attachment{w: response, format: format, filename: "trace." + format.Ext}

		written, err := download.Write(payload)
		Expect(err).ToNot(HaveOccurred())
		Expect(written).To(Equal(len(payload)))
		Expect(response.Result().StatusCode).To(Equal(http.StatusOK))
		Expect(response.Result().Header).To(SatisfyAll(
			HaveKeyWithValue("Content-Type", []string{contentType}),
			HaveKeyWithValue("Content-Disposition", []string{`attachment; filename="trace.` + format.Ext + `"`}),
			HaveKeyWithValue("X-Content-Type-Options", []string{"nosniff"}),
			HaveKeyWithValue("Content-Security-Policy", []string{"sandbox"}),
		))
		Expect(response.Body.Bytes()).To(Equal(payload))
	},
		ginkgo.Entry("NDJSON", tracing.NDJSON, "application/x-ndjson"),
		ginkgo.Entry("HAR JSON", tracing.ExportFormat{Name: "har", Ext: "har", MIME: "application/json"}, "application/json"),
		ginkgo.Entry("terminal cast", tracing.ExportFormat{Name: "cast", Ext: "cast", MIME: "application/x-asciicast"}, "application/x-asciicast"),
		ginkgo.Entry("HTML", tracing.ExportFormat{Name: "html", Ext: "html", MIME: "text/html"}, "application/octet-stream"),
	)
})
