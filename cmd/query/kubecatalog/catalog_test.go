package kubecatalog

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("Kubernetes catalog", func() {
	It("lists pod and daemonset targets with their container ports", func() {
		client := fake.NewSimpleClientset(
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "api-abc12", Namespace: "payments"},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
				}}},
			},
			&appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{Name: "node-agent", Namespace: "payments"},
				Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9090}}}},
				}}},
			},
		)

		workloads, err := ListWorkloads(context.Background(), client, "payments", "pod,daemonset")
		Expect(err).NotTo(HaveOccurred())
		Expect(workloads).To(Equal(map[string][]Workload{
			"pod":       {{Name: "api-abc12", Ports: []Port{{Name: "http", Number: 8080}}}},
			"daemonset": {{Name: "node-agent", Ports: []Port{{Name: "metrics", Number: 9090}}}},
		}))
	})

	It("normalizes requested kinds and defaults invalid input to every supported kind", func() {
		Expect(parseWorkloadKinds(" Pod,DAEMONSET,pod ")).To(Equal([]string{"pod", "daemonset"}))
		Expect(parseWorkloadKinds("not-a-kind")).To(Equal(AllWorkloadKinds))
	})
})
