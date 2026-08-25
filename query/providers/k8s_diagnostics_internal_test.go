package providers

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/flanksource/commons-db/logs/k8s"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Kubernetes log diagnostics", func() {
	It("sorts and bounds resolved pod names while retaining the full count", func() {
		pods := make([]corev1.Pod, 24)
		for index := range pods {
			pods[index] = corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Namespace: "prod",
				Name:      fmt.Sprintf("api-%02d", len(pods)-index-1),
			}}
		}

		details := kubernetesLogRequestDetails(pods, k8s.Request{})
		listed := details["pods"].([]string)
		Expect(details).To(SatisfyAll(
			HaveKeyWithValue("pod_count", 24),
			HaveKeyWithValue("pods_truncated", true),
			HaveKeyWithValue("namespaces", []string{"prod"}),
		))
		Expect(listed).To(SatisfyAll(
			HaveLen(kubernetesDiagnosticListLimit),
			HaveExactElements(
				"prod/api-00", "prod/api-01", "prod/api-02", "prod/api-03", "prod/api-04",
				"prod/api-05", "prod/api-06", "prod/api-07", "prod/api-08", "prod/api-09",
				"prod/api-10", "prod/api-11", "prod/api-12", "prod/api-13", "prod/api-14",
				"prod/api-15", "prod/api-16", "prod/api-17", "prod/api-18", "prod/api-19",
			),
		))
	})
})
