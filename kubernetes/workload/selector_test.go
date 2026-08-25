package workload_test

import (
	"github.com/flanksource/commons-db/kubernetes/workload"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Target selector", func() {
	resources := []workload.Resource{
		{
			Kind: "Deployment", Namespace: "payments", Name: "api",
			UID: "uid-api", Labels: map[string]string{
				"app.kubernetes.io/name": "payments-api",
				"tier":                   "backend",
				"version":                "api.v2",
			},
		},
		{
			Kind: "StatefulSet", Namespace: "payments", Name: "worker",
			UID: "uid-worker", Labels: map[string]string{
				"app.kubernetes.io/name": "payments-worker",
				"tier":                   "backend",
			},
		},
		{
			Kind: "Deployment", Namespace: "platform", Name: "api",
			UID: "uid-platform", Labels: map[string]string{"tier": "frontend"},
		},
	}

	It("matches identity, wildcard, UID, and label predicates", func() {
		selector, err := workload.ParseSelector(
			"kind=Deployment namespace=pay* labels.app.kubernetes.io/name=payments-api uid=uid-api",
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(selector.Filter(resources)).To(Equal(resources[:1]))
	})

	It("supports grouped OR while an added selector can only narrow it", func() {
		base, err := workload.ParseSelector(
			"namespace=payments (kind=Deployment | kind=StatefulSet) labels.tier=backend",
		)
		Expect(err).ToNot(HaveOccurred())
		runtime, err := workload.ParseSelector("name=worker")
		Expect(err).ToNot(HaveOccurred())

		Expect(base.And(runtime).Filter(resources)).To(Equal(resources[1:2]))
	})

	It("accepts dotted Kubernetes label values", func() {
		selector, err := workload.ParseSelector("labels.version=api.v2")
		Expect(err).ToNot(HaveOccurred())
		Expect(selector.Filter(resources)).To(Equal(resources[:1]))
	})

	It("uses comma values as alternatives and exclusions as not-in", func() {
		selector, err := workload.ParseSelector(
			"kind=Deployment,StatefulSet labels.app.kubernetes.io/name!=payments-worker",
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(selector.Filter(resources)).To(ConsistOf(resources[0], resources[2]))
	})

	It("treats label star predicates as existence checks", func() {
		present, err := workload.ParseSelector("labels.app.kubernetes.io/name=*")
		Expect(err).ToNot(HaveOccurred())
		missing, err := workload.ParseSelector("labels.app.kubernetes.io/name!=*")
		Expect(err).ToNot(HaveOccurred())

		Expect(present.Filter(resources)).To(ConsistOf(resources[0], resources[1]))
		Expect(missing.Filter(resources)).To(Equal(resources[2:]))
	})

	It("recognizes exact workload identity", func() {
		uid, err := workload.ParseSelector("uid=uid-api")
		Expect(err).ToNot(HaveOccurred())
		identity, err := workload.ParseSelector(
			"kind=Deployment namespace=payments name=api",
		)
		Expect(err).ToNot(HaveOccurred())
		broad, err := workload.ParseSelector("namespace=payments")
		Expect(err).ToNot(HaveOccurred())

		Expect(uid.Exact()).To(BeTrue())
		Expect(identity.Exact()).To(BeTrue())
		Expect(broad.Exact()).To(BeFalse())
	})

	It("rejects unsupported fields and operators", func() {
		_, err := workload.ParseSelector("cluster=prod")
		Expect(err).To(MatchError(ContainSubstring("field \"cluster\"")))
		_, err = workload.ParseSelector("name>api")
		Expect(err).To(MatchError(ContainSubstring("operator \">\"")))
	})
})
