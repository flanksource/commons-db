package kubernetes

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestServiceTargetPortResolvesNumericAndNamedTargets(t *testing.T) {
	pod := readyPod("db-0", corev1.ContainerPort{Name: "postgres", ContainerPort: 15432})
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "database"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
			{Port: 5432, TargetPort: intstr.FromInt32(15432)},
			{Port: 6432, TargetPort: intstr.FromString("postgres")},
		}},
	}
	if got, err := serviceTargetPort(service, &pod, 5432); err != nil || got != 15432 {
		t.Fatalf("numeric target = %d, %v", got, err)
	}
	if got, err := serviceTargetPort(service, &pod, 6432); err != nil || got != 15432 {
		t.Fatalf("named target = %d, %v", got, err)
	}
	if _, err := serviceTargetPort(service, &pod, 9999); err == nil {
		t.Fatal("expected missing service port error")
	}
}

func TestSelectReadyPodIsDeterministicAndSkipsUnreadyPods(t *testing.T) {
	unready := readyPod("a-unready", corev1.ContainerPort{ContainerPort: 5432})
	unready.Status.Conditions[0].Status = corev1.ConditionFalse
	readyZ := readyPod("z-ready", corev1.ContainerPort{ContainerPort: 5432})
	readyB := readyPod("b-ready", corev1.ContainerPort{ContainerPort: 5432})

	got, err := selectReadyPod([]corev1.Pod{readyZ, unready, readyB})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "b-ready" {
		t.Fatalf("selected pod = %s, want b-ready", got.Name)
	}
}

func readyPod(name string, ports ...corev1.ContainerPort) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "database", Ports: ports}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}
