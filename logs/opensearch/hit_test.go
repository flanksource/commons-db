package opensearch

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Raw span hit numbers", func() {
	It("preserves nanoseconds in source, returned fields and paging keys", func() {
		var hit SearchHit
		Expect(json.Unmarshal([]byte(`{"_id":"span","_source":{"clock":1793613600000000123},"fields":{"clock":[1793613600000000123]},"sort":[1793613600000000123]}`), &hit)).To(Succeed())
		Expect(hit.Source["clock"]).To(Equal(int64(1793613600000000123)))
		Expect(hit.Fields["clock"]).To(Equal([]any{int64(1793613600000000123)}))
		Expect(hit.Sort).To(Equal([]any{int64(1793613600000000123)}))
	})
})
