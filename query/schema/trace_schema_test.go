package schema_test

import (
	"github.com/flanksource/commons-db/query/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Profile trace schema", func() {
	It("exposes the raw-row processor buffer bounds", func() {
		properties := schema.ProfileSource()["properties"].(schema.Schema)
		trace := properties["trace"].(schema.Schema)
		buffer := trace["properties"].(schema.Schema)["buffer"].(schema.Schema)
		bounds := buffer["properties"].(schema.Schema)

		Expect(buffer["anyOf"]).To(HaveLen(2))
		Expect(bounds["maxRows"].(schema.Schema)).To(HaveKeyWithValue("minimum", 1))
		Expect(bounds["maxWait"].(schema.Schema)).To(HaveKeyWithValue("type", "string"))
	})
})
