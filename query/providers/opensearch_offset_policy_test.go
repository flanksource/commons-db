package providers_test

import (
	"fmt"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OpenSearch bounded paging", func() {
	It("samples one page without a backend cursor regardless of the matching total", func() {
		stub := newOpenSearchStub(func(int) string {
			return `{"hits":{"total":{"value":1000000,"relation":"eq"},"hits":[{"_id":"one","_source":{"message":"entry"},"sort":[1]}]}}`
		})
		defer stub.server.Close()
		result, err := query.Sample(context.New(), stub.profile("sample", query.Order{{Column: "message", Unique: true}}), query.SampleOptions{Page: query.PageRequest{Limit: 1}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))
		Expect(stub.openedScrolls).To(BeZero())
		Expect(stub.openedPITs).To(BeZero())
	})
	It("resumes a stateless page at its next offset without opening a scroll", func() {
		stub := newOpenSearchStub(func(call int) string {
			return fmt.Sprintf(`{"hits":{"total":{"value":1000000,"relation":"eq"},"hits":[{"_id":"%d","_source":{"message":"entry"},"sort":[%d]}]}}`, call, call)
		})
		defer stub.server.Close()
		profile := stub.profile("bounded", query.Order{{Column: "message", Unique: true}})
		var cursor query.Cursor
		for page, err := range query.ExecutePages(context.New(), profile, query.PageRequest{Limit: 1}) {
			Expect(err).NotTo(HaveOccurred())
			cursor = page.Next
			break
		}
		Expect(cursor).NotTo(BeEmpty())
		for _, err := range query.ExecutePages(context.New(), profile, query.PageRequest{Limit: 1, Cursor: cursor}) {
			Expect(err).NotTo(HaveOccurred())
			break
		}
		Expect(stub.bodies[1]["from"]).To(Equal(float64(1)))
		Expect(stub.openedScrolls).To(BeZero())
		Expect(stub.openedPITs).To(BeZero())
	})
})
