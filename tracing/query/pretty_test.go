package query_test

import (
	"encoding/json"
	"strings"

	tracequery "github.com/flanksource/commons-db/tracing/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("query record presentation", func() {
	It("sorts dynamic fields without changing their stored values", func() {
		r := tracequery.Row{Profile: "request-rate", Provider: "prometheus", Row: map[string]any{"z": 9, "a": 2}}
		before, err := json.Marshal(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Join(strings.Fields(r.Pretty().String()), " ")).To(Equal("request-rate prometheus a=2 z=9"))
		Expect(r.PrettyFull().String()).To(ContainSubstring("request-rate"))
		after, err := json.Marshal(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(after).To(Equal(before))
	})
})
