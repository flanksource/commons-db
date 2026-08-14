package profiles

import (
	"encoding/json"
	"net/http"

	dbcontext "github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("profile sampling errors", func() {
	It("returns an Oops JSON error with context for an invalid request", func() {
		handler := newProfileSampleHandler("/api/v1", dbcontext.New(), http.NotFoundHandler())
		response := post(handler, "/api/v1/profile/sample", `{"profile":{"profile":"sample","_id":"ui-only"}}`)

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Header().Get("Content-Type")).To(Equal("application/json"))

		var payload map[string]any
		Expect(json.Unmarshal(response.Body.Bytes(), &payload)).To(Succeed())
		Expect(payload).To(HaveKeyWithValue("error", ContainSubstring(`unknown field "_id"`)))
		Expect(payload).To(HaveKeyWithValue("trace", Not(BeEmpty())))
		Expect(payload).To(HaveKeyWithValue("stacktrace", Not(BeEmpty())))
		Expect(payload).To(HaveKeyWithValue("context", HaveKeyWithValue("operation", "profile.sample")))
	})

	It("accepts the explicit processor preview mode", func() {
		handler := newProfileSampleHandler("/api/v1", dbcontext.New(), http.NotFoundHandler())
		response := post(handler, "/api/v1/profile/sample", `{
			"profile":{"profile":"sample","provider":{"type":"custom"}},
			"previewProcessors":true
		}`)

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Body.String()).To(ContainSubstring(`sampling provider \"custom\" is disabled`))
		Expect(response.Body.String()).NotTo(ContainSubstring(`unknown field \"previewProcessors\"`))
	})
})
