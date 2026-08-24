package workload_test

import (
	"context"

	"github.com/flanksource/commons-db/kubernetes/workload"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("Workload catalog", func() {
	client := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "api", Namespace: "payments", UID: types.UID("deploy-api"),
				Labels: map[string]string{"app": "api", "tier": "backend"},
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "web", Namespace: "platform", UID: types.UID("deploy-web"),
				Labels: map[string]string{"app": "web", "tier": "frontend"},
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "api-1", Namespace: "payments", UID: types.UID("pod-api"),
				Labels: map[string]string{"app": "api"},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: "api", Ports: []corev1.ContainerPort{{
					Name: "http", ContainerPort: intstr.FromInt32(8080).IntVal,
				}},
			}}},
		},
	)

	It("returns resources matching the grammar selector with identity metadata", func() {
		selector, err := workload.ParseSelector(
			"kind=Deployment namespace=payments labels.tier=backend",
		)
		Expect(err).ToNot(HaveOccurred())

		result, err := workload.List(context.Background(), client, workload.ListOptions{
			Selector: selector,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Total).To(Equal(1))
		Expect(result.Truncated).To(BeFalse())
		Expect(result.Resources).To(HaveLen(1))
		Expect(result.Resources[0].Kind).To(Equal("Deployment"))
		Expect(result.Resources[0].Namespace).To(Equal("payments"))
		Expect(result.Resources[0].Name).To(Equal("api"))
		Expect(result.Resources[0].UID).To(Equal("deploy-api"))
		Expect(result.Resources[0].Labels).To(Equal(map[string]string{
			"app": "api", "tier": "backend",
		}))
	})

	It("selects a deterministic first N and reports truncation", func() {
		selector, err := workload.ParseSelector("kind=Deployment")
		Expect(err).ToNot(HaveOccurred())

		result, err := workload.List(context.Background(), client, workload.ListOptions{
			Selector: selector,
			Limit:    1,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Total).To(Equal(2))
		Expect(result.Truncated).To(BeTrue())
		Expect(result.Resources).To(HaveLen(1))
		Expect(result.Resources[0].Namespace).To(Equal("payments"))
	})

	It("expands controllers and direct pod matches without duplicate pods", func() {
		selector, err := workload.ParseSelector(
			"(kind=Deployment name=api) | (kind=Pod name=api-1)",
		)
		Expect(err).ToNot(HaveOccurred())
		result, err := workload.List(context.Background(), client, workload.ListOptions{
			Selector: selector,
		})
		Expect(err).ToNot(HaveOccurred())

		pods, err := workload.ResolvePods(context.Background(), client, result.Resources)
		Expect(err).ToNot(HaveOccurred())
		Expect(pods).To(HaveLen(1))
		Expect(pods[0].UID).To(Equal(types.UID("pod-api")))
	})
})
