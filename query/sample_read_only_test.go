package query

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("profile sample read-only validation", func() {
	It("allows Kubernetes log reads", func() {
		Expect(validateSampleReadOnly("k8s", "", map[string]any{
			"kind": "Pod", "namespace": "payments", "name": "api-abc12",
		})).To(Succeed())
	})
})
