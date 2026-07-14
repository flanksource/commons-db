package app

import (
	"context"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestParseWorkloadKinds(t *testing.T) {
	cases := map[string][]string{
		"":                         allWorkloadKinds,
		"   ":                      allWorkloadKinds,
		"service":                  {"service"},
		"service,ingress":          {"service", "ingress"},
		" Service , INGRESS ":      {"service", "ingress"},
		"service,service":          {"service"},
		"bogus":                    allWorkloadKinds,
		"deployment,bogus,service": {"deployment", "service"},
	}
	for in, want := range cases {
		if got := parseWorkloadKinds(in); !reflect.DeepEqual(got, want) {
			t.Errorf("parseWorkloadKinds(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestListNamespaces(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "prod"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
	)
	got, err := listNamespaces(context.Background(), client)
	if err != nil {
		t.Fatalf("listNamespaces: %v", err)
	}
	if want := []string{"default", "prod"}; !reflect.DeepEqual(got, want) {
		t.Errorf("listNamespaces = %v, want %v (sorted)", got, want)
	}
}

func TestListWorkloads(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod"},
			Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "pg", Port: 5432}}},
		},
		&networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"},
			Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "app.example.com"}}},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}}},
			}}},
		},
	)

	got, err := listWorkloads(context.Background(), client, "prod", "service,ingress,deployment")
	if err != nil {
		t.Fatalf("listWorkloads: %v", err)
	}

	want := map[string][]workloadResource{
		"service":    {{Name: "db", Ports: []workloadPort{{Name: "pg", Number: 5432}}}},
		"ingress":    {{Name: "web", Hosts: []string{"app.example.com"}}},
		"deployment": {{Name: "api", Ports: []workloadPort{{Name: "http", Number: 8080}}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listWorkloads = %#v, want %#v", got, want)
	}
}
