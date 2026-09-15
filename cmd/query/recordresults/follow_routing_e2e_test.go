package recordresults_test

import (
	"encoding/json"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

var _ = Describe("following a record result type whose source routes by request", func() {
	It("follows the stream of the tenant that started the follow, and no other tenant's", func() {
		schemas := recordstore.NewSchemas()
		server := newFollowServerWith(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: localSettings(""), Source: kvRouter(schemas),
			Schemas: schemas, Register: registerFollowTypes,
		})
		appendAs := func(tenant string, first, last int) {
			_, err := recordstore.AppendTyped(forTenant(tenant), server.results.Backend, "run-1", "sample_event", sampleEvents(first, last))
			Expect(err).ToNot(HaveOccurred())
		}
		appendAs("a", 1, 2)

		status, body := server.startFollowAs("a", "follow=true&stream=run-1&afterSeq=2")
		Expect(status).To(Equal(http.StatusCreated), body)
		var info query.SessionInfo
		Expect(json.Unmarshal([]byte(body), &info)).To(Succeed())
		events, _ := server.subscribe(info.ID, "")

		appendAs("a", 3, 4)
		rows, _ := rowsFrom(events, 2)
		Expect(seqs(rows)).To(Equal([]string{"3", "4"}))

		By("answering tenant b's follow of the same stream id as a stream that does not exist")
		status, body = server.startFollowAs("b", "follow=true&stream=run-1")
		Expect(status).To(Equal(http.StatusNotFound), body)
		Expect(body).To(ContainSubstring("run-1"))
	})
})
