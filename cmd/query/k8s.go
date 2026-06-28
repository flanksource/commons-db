package main

import (
	gocontext "context"
	"fmt"
	"slices"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// workloadPort is one exposed port of a workload/service.
type workloadPort struct {
	Name   string `json:"name,omitempty"`
	Number int32  `json:"number"`
}

// workloadResource matches clicky-ui's WorkloadResource: a workload's name plus
// the ports (services/deployments/statefulsets) or hosts (ingresses) the form's
// WorkloadPicker offers when building a connection URL.
type workloadResource struct {
	Name  string         `json:"name"`
	Ports []workloadPort `json:"ports,omitempty"`
	Hosts []string       `json:"hosts,omitempty"`
}

var allWorkloadKinds = []string{"service", "ingress", "deployment", "statefulset"}

// listNamespaces returns every namespace name, sorted, for the NamespacePicker.
func listNamespaces(ctx gocontext.Context, client kubernetes.Interface) ([]string, error) {
	list, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	out := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		out = append(out, ns.Name)
	}
	sort.Strings(out)
	return out, nil
}

// parseWorkloadKinds normalises the comma-separated kinds query param, defaulting
// to all supported kinds.
func parseWorkloadKinds(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return allWorkloadKinds
	}
	var kinds []string
	for _, k := range strings.Split(raw, ",") {
		k = strings.ToLower(strings.TrimSpace(k))
		if slices.Contains(allWorkloadKinds, k) && !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 {
		return allWorkloadKinds
	}
	return kinds
}

// listWorkloads returns the requested workload kinds in a namespace, keyed by
// kind, for the WorkloadPicker (the shape clicky-ui's loadWorkloads expects).
func listWorkloads(ctx gocontext.Context, client kubernetes.Interface, ns, kindsParam string) (map[string][]workloadResource, error) {
	out := map[string][]workloadResource{}
	for _, kind := range parseWorkloadKinds(kindsParam) {
		var (
			res []workloadResource
			err error
		)
		switch kind {
		case "service":
			res, err = listServices(ctx, client, ns)
		case "ingress":
			res, err = listIngresses(ctx, client, ns)
		case "deployment":
			res, err = listDeployments(ctx, client, ns)
		case "statefulset":
			res, err = listStatefulSets(ctx, client, ns)
		}
		if err != nil {
			return nil, err
		}
		out[kind] = res
	}
	return out, nil
}

func listServices(ctx gocontext.Context, client kubernetes.Interface, ns string) ([]workloadResource, error) {
	list, err := client.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list services in %q: %w", ns, err)
	}
	out := make([]workloadResource, 0, len(list.Items))
	for _, s := range list.Items {
		r := workloadResource{Name: s.Name}
		for _, p := range s.Spec.Ports {
			r.Ports = append(r.Ports, workloadPort{Name: p.Name, Number: p.Port})
		}
		out = append(out, r)
	}
	return sortWorkloads(out), nil
}

func listIngresses(ctx gocontext.Context, client kubernetes.Interface, ns string) ([]workloadResource, error) {
	list, err := client.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list ingresses in %q: %w", ns, err)
	}
	out := make([]workloadResource, 0, len(list.Items))
	for _, ing := range list.Items {
		r := workloadResource{Name: ing.Name}
		for _, rule := range ing.Spec.Rules {
			if rule.Host != "" {
				r.Hosts = append(r.Hosts, rule.Host)
			}
		}
		out = append(out, r)
	}
	return sortWorkloads(out), nil
}

func listDeployments(ctx gocontext.Context, client kubernetes.Interface, ns string) ([]workloadResource, error) {
	list, err := client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list deployments in %q: %w", ns, err)
	}
	out := make([]workloadResource, 0, len(list.Items))
	for _, d := range list.Items {
		out = append(out, workloadResource{Name: d.Name, Ports: containerPorts(d.Spec.Template.Spec.Containers)})
	}
	return sortWorkloads(out), nil
}

func listStatefulSets(ctx gocontext.Context, client kubernetes.Interface, ns string) ([]workloadResource, error) {
	list, err := client.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list statefulsets in %q: %w", ns, err)
	}
	out := make([]workloadResource, 0, len(list.Items))
	for _, s := range list.Items {
		out = append(out, workloadResource{Name: s.Name, Ports: containerPorts(s.Spec.Template.Spec.Containers)})
	}
	return sortWorkloads(out), nil
}

func containerPorts(containers []corev1.Container) []workloadPort {
	var ports []workloadPort
	for _, c := range containers {
		for _, p := range c.Ports {
			ports = append(ports, workloadPort{Name: p.Name, Number: p.ContainerPort})
		}
	}
	return ports
}

func sortWorkloads(in []workloadResource) []workloadResource {
	sort.Slice(in, func(i, j int) bool { return in[i].Name < in[j].Name })
	return in
}
