package esdsl

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var _ = Describe("Search import", func() {
	It("round-trips nested boolean conditions and qualifiers through native DSL", func() {
		body := []byte(`{"query":{"bool":{"filter":[{"term":{"service":{"value":"checkout","case_insensitive":true}}},{"range":{"duration_ms":{"gte":2.5}}}],"must_not":[{"exists":{"field":"parent_id"}}]}},"sort":[{"timestamp":{"order":"desc"}}],"size":25}`)
		parsed, err := Parse(body)
		Expect(err).NotTo(HaveOccurred())
		compiled, err := Compile(CompileRequest{Search: parsed})
		Expect(err).NotTo(HaveOccurred())
		Expect(compiled.Size).To(Equal(25))
		compiled.Body["size"] = compiled.Size
		encoded, err := json.Marshal(compiled.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(encoded).To(MatchJSON(body))
	})
	It("preserves canonical parameter references in YAML", func() {
		search := Search{TimeField: "timestamp", Query: &Condition{Op: OpTerm, Field: "service", Value: Param("service")}}
		encoded, err := yaml.Marshal(search)
		Expect(err).NotTo(HaveOccurred())
		var restored Search
		Expect(yaml.Unmarshal(encoded, &restored)).To(Succeed())
		Expect(restored).To(Equal(search))
	})
	It("rejects unknown clauses and qualifiers instead of discarding them", func() {
		for _, body := range []string{`{"query":{"script":{"script":"true"}}}`, `{"query":{"term":{"service":{"value":"checkout","unexpected":true}}}}`, `{"aggs":{"services":{"terms":{"field":"service"}}}}`} {
			_, err := Parse([]byte(body))
			Expect(err).To(HaveOccurred())
		}
	})
})
