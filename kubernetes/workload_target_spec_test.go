package kubernetes

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("ResolvePodPort", func() {
	const namespace = "shop"
	appLabels := map[string]string{"app": "orders"}
	selector := &metav1.LabelSelector{MatchLabels: appLabels}
	httpPort := corev1.ContainerPort{Name: "http", ContainerPort: 8080}

	pod := func(name string, ready bool) *corev1.Pod {
		p := readyPod(name, httpPort)
		p.Namespace, p.Labels = namespace, appLabels
		if !ready {
			p.Status.Conditions[0].Status = corev1.ConditionFalse
		}
		return &p
	}
	meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: name, Namespace: namespace} }

	newClient := func(pods ...*corev1.Pod) *fake.Clientset {
		clientset := fake.NewSimpleClientset(
			&appsv1.Deployment{ObjectMeta: meta("orders"), Spec: appsv1.DeploymentSpec{Selector: selector}},
			&appsv1.StatefulSet{ObjectMeta: meta("orders"), Spec: appsv1.StatefulSetSpec{Selector: selector}},
			&appsv1.DaemonSet{ObjectMeta: meta("orders"), Spec: appsv1.DaemonSetSpec{Selector: selector}},
			&appsv1.ReplicaSet{ObjectMeta: meta("orders"), Spec: appsv1.ReplicaSetSpec{Selector: selector}},
			&corev1.Service{ObjectMeta: meta("orders"), Spec: corev1.ServiceSpec{
				Selector: appLabels,
				Ports:    []corev1.ServicePort{{Name: "web", Port: 80, TargetPort: intstr.FromString("http")}},
			}},
		)
		for _, p := range pods {
			Expect(clientset.Tracker().Add(p)).To(Succeed())
		}
		return clientset
	}

	DescribeTable("resolves a workload to the first Ready pod by name and a numeric port",
		func(target WorkloadTarget, expected PodPort) {
			got, err := ResolvePodPort(context.Background(), newClient(pod("orders-0", false), pod("orders-c", true), pod("orders-b", true)), target)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(expected))
		},
		Entry("pod by name", WorkloadTarget{Namespace: namespace, Kind: "pod", Name: "orders-c", Port: "8080"}, PodPort{Namespace: namespace, Pod: "orders-c", Port: 8080}),
		Entry("deployment", WorkloadTarget{Namespace: namespace, Kind: "deployment", Name: "orders", Port: "8080"}, PodPort{Namespace: namespace, Pod: "orders-b", Port: 8080}),
		Entry("statefulset", WorkloadTarget{Namespace: namespace, Kind: "statefulset", Name: "orders", Port: "8080"}, PodPort{Namespace: namespace, Pod: "orders-b", Port: 8080}),
		Entry("daemonset", WorkloadTarget{Namespace: namespace, Kind: "daemonset", Name: "orders"}, PodPort{Namespace: namespace, Pod: "orders-b", Port: 8080}),
		Entry("replicaset with a named container port", WorkloadTarget{Namespace: namespace, Kind: "replicaset", Name: "orders", Port: "http"}, PodPort{Namespace: namespace, Pod: "orders-b", Port: 8080}),
		Entry("explicit pod selector", WorkloadTarget{Namespace: namespace, Kind: "deployment", LabelSelector: "app=orders", Port: "http"}, PodPort{Namespace: namespace, Pod: "orders-b", Port: 8080}),
		Entry("service with a named service port", WorkloadTarget{Namespace: namespace, Kind: "service", Name: "orders", Port: "web"}, PodPort{Namespace: namespace, Pod: "orders-b", Port: 8080}),
	)

	It("fails naming the workload and namespace when no pod is Ready", func() {
		_, err := ResolvePodPort(context.Background(), newClient(pod("orders-0", false)), WorkloadTarget{Namespace: namespace, Kind: "statefulset", Name: "orders"})
		Expect(err).To(MatchError(ContainSubstring("statefulset/orders in namespace shop")))
		Expect(err).To(MatchError(ContainSubstring("no running, ready pod")))
	})

	It("rejects an unknown kind and lists the supported ones", func() {
		_, err := ResolvePodPort(context.Background(), newClient(), WorkloadTarget{Namespace: namespace, Kind: "cronjob", Name: "orders"})
		Expect(err).To(MatchError(ContainSubstring(`kind "cronjob" is not one of daemonset, deployment, pod, replicaset, service, statefulset`)))
	})

	It("rejects a port name no container declares", func() {
		_, err := ResolvePodPort(context.Background(), newClient(pod("orders-b", true)), WorkloadTarget{Namespace: namespace, Kind: "deployment", Name: "orders", Port: "metrics"})
		Expect(err).To(MatchError(ContainSubstring(`pod orders-b has no container port named "metrics"`)))
	})
})
