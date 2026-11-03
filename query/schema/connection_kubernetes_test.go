package schema_test

import (
	"github.com/flanksource/commons-db/query/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Kubernetes selection layout", func() {
	It("keeps the selectors together before logging without narrowing other provider fields", func() {
		component := schema.ConnectionComponents()["kubernetes"]["properties"].(schema.Schema)
		properties := component["properties"].(schema.Schema)
		Expect(properties).To(HaveKeyWithValue("x-columns", 2))
		Expect(properties).To(HaveKeyWithValue("x-clicky-order", 7))
		fields := properties["properties"].(schema.Schema)
		Expect(fields["kubeconfig"].(schema.Schema)).NotTo(HaveKeyWithValue("x-col-span", "full"))
		Expect(fields["kubecontext"].(schema.Schema)).NotTo(HaveKeyWithValue("x-col-span", "full"))
		Expect(fields["max_resources"].(schema.Schema)).To(HaveKeyWithValue("x-col-span", "full"))
	})
})
