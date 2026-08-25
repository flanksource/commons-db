package kubecatalog

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Port struct {
	Name   string `json:"name,omitempty"`
	Number int32  `json:"number"`
}

type Workload struct {
	Name  string   `json:"name"`
	Ports []Port   `json:"ports,omitempty"`
	Hosts []string `json:"hosts,omitempty"`
}

var AllWorkloadKinds = []string{
	"service",
	"ingress",
	"pod",
	"deployment",
	"statefulset",
	"daemonset",
}

func ListNamespaces(ctx context.Context, client kubernetes.Interface) ([]string, error) {
	list, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	out := make([]string, 0, len(list.Items))
	for _, namespace := range list.Items {
		out = append(out, namespace.Name)
	}
	sort.Strings(out)
	return out, nil
}

func parseWorkloadKinds(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return AllWorkloadKinds
	}
	var kinds []string
	for _, kind := range strings.Split(raw, ",") {
		kind = strings.ToLower(strings.TrimSpace(kind))
		if slices.Contains(AllWorkloadKinds, kind) && !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) == 0 {
		return AllWorkloadKinds
	}
	return kinds
}

func ListWorkloads(
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
	kindsParam string,
) (map[string][]Workload, error) {
	out := map[string][]Workload{}
	for _, kind := range parseWorkloadKinds(kindsParam) {
		var (
			resources []Workload
			err       error
		)
		switch kind {
		case "service":
			resources, err = listServices(ctx, client, namespace)
		case "ingress":
			resources, err = listIngresses(ctx, client, namespace)
		case "pod":
			resources, err = listPods(ctx, client, namespace)
		case "deployment":
			resources, err = listDeployments(ctx, client, namespace)
		case "statefulset":
			resources, err = listStatefulSets(ctx, client, namespace)
		case "daemonset":
			resources, err = listDaemonSets(ctx, client, namespace)
		}
		if err != nil {
			return nil, err
		}
		out[kind] = resources
	}
	return out, nil
}

func listServices(ctx context.Context, client kubernetes.Interface, namespace string) ([]Workload, error) {
	list, err := client.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list services in %q: %w", namespace, err)
	}
	out := make([]Workload, 0, len(list.Items))
	for _, service := range list.Items {
		resource := Workload{Name: service.Name}
		for _, port := range service.Spec.Ports {
			resource.Ports = append(resource.Ports, Port{Name: port.Name, Number: port.Port})
		}
		out = append(out, resource)
	}
	return sortWorkloads(out), nil
}

func listIngresses(ctx context.Context, client kubernetes.Interface, namespace string) ([]Workload, error) {
	list, err := client.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list ingresses in %q: %w", namespace, err)
	}
	out := make([]Workload, 0, len(list.Items))
	for _, ingress := range list.Items {
		resource := Workload{Name: ingress.Name}
		for _, rule := range ingress.Spec.Rules {
			if rule.Host != "" {
				resource.Hosts = append(resource.Hosts, rule.Host)
			}
		}
		out = append(out, resource)
	}
	return sortWorkloads(out), nil
}

func listPods(ctx context.Context, client kubernetes.Interface, namespace string) ([]Workload, error) {
	list, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods in %q: %w", namespace, err)
	}
	out := make([]Workload, 0, len(list.Items))
	for _, pod := range list.Items {
		out = append(out, Workload{Name: pod.Name, Ports: containerPorts(pod.Spec.Containers)})
	}
	return sortWorkloads(out), nil
}

func listDeployments(ctx context.Context, client kubernetes.Interface, namespace string) ([]Workload, error) {
	list, err := client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list deployments in %q: %w", namespace, err)
	}
	out := make([]Workload, 0, len(list.Items))
	for _, deployment := range list.Items {
		out = append(out, Workload{Name: deployment.Name, Ports: containerPorts(deployment.Spec.Template.Spec.Containers)})
	}
	return sortWorkloads(out), nil
}

func listStatefulSets(ctx context.Context, client kubernetes.Interface, namespace string) ([]Workload, error) {
	list, err := client.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list statefulsets in %q: %w", namespace, err)
	}
	out := make([]Workload, 0, len(list.Items))
	for _, statefulSet := range list.Items {
		out = append(out, Workload{Name: statefulSet.Name, Ports: containerPorts(statefulSet.Spec.Template.Spec.Containers)})
	}
	return sortWorkloads(out), nil
}

func listDaemonSets(ctx context.Context, client kubernetes.Interface, namespace string) ([]Workload, error) {
	list, err := client.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list daemonsets in %q: %w", namespace, err)
	}
	out := make([]Workload, 0, len(list.Items))
	for _, daemonSet := range list.Items {
		out = append(out, Workload{Name: daemonSet.Name, Ports: containerPorts(daemonSet.Spec.Template.Spec.Containers)})
	}
	return sortWorkloads(out), nil
}

func containerPorts(containers []corev1.Container) []Port {
	var ports []Port
	for _, container := range containers {
		for _, port := range container.Ports {
			ports = append(ports, Port{Name: port.Name, Number: port.ContainerPort})
		}
	}
	return ports
}

func sortWorkloads(resources []Workload) []Workload {
	sort.Slice(resources, func(i, j int) bool { return resources[i].Name < resources[j].Name })
	return resources
}
