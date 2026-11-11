package providers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/kubernetes/workload"
	"github.com/flanksource/commons-db/query"
)

var _ = Describe("Kubernetes namespace filter", func() {
	resources := []workload.Resource{
		{Kind: "Deployment", Namespace: "payments", Name: "api"},
		{Kind: "Deployment", Namespace: "billing", Name: "worker"},
		{Kind: "Deployment", Namespace: "kube-system", Name: "coredns"},
	}
	names := func(selector workload.Selector) []string {
		matched := []string{}
		for _, resource := range selector.Filter(resources) {
			matched = append(matched, resource.Name)
		}
		return matched
	}

	It("narrows an open scope to the included namespaces and away from the excluded ones", func() {
		included, err := kubernetesTargetSelector("", []query.ColumnFilterValue{
			{Key: "namespace", Field: "namespace", Kind: query.ColumnFilterKindTerms, Include: []string{"payments", "billing"}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(names(included)).To(ConsistOf("api", "worker"))

		excluded, err := kubernetesTargetSelector("", []query.ColumnFilterValue{
			{Key: "namespace", Field: "namespace", Kind: query.ColumnFilterKindTerms, Exclude: []string{"kube-system"}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(names(excluded)).To(ConsistOf("api", "worker"))
	})

	It("offers the cluster's namespaces matching the search, in name order", func() {
		client := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments"}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "billing"}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		)
		options, err := kubernetesNamespaceOptions(context.New(), client, "ing")
		Expect(err).ToNot(HaveOccurred())
		Expect(options).To(Equal([]query.FilterOption{{Value: "billing"}}))

		all, err := kubernetesNamespaceOptions(context.New(), client, "")
		Expect(err).ToNot(HaveOccurred())
		Expect(all).To(Equal([]query.FilterOption{{Value: "billing"}, {Value: "kube-system"}, {Value: "payments"}}))
	})
})
