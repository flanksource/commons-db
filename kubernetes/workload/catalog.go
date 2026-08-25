package workload

import (
	"context"
	"fmt"
	"maps"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var LogKinds = []string{"Pod", "Deployment", "StatefulSet", "DaemonSet"}

type ListOptions struct {
	Selector Selector
	Limit    int
}

type ListResult struct {
	Resources []Resource
	Total     int
	Truncated bool
}

func List(ctx context.Context, client kubernetes.Interface, options ListOptions) (ListResult, error) {
	resources, err := listResources(ctx, client)
	if err != nil {
		return ListResult{}, err
	}
	resources = options.Selector.Filter(resources)
	sortResources(resources)
	result := ListResult{Resources: resources, Total: len(resources)}
	if options.Limit > 0 && len(result.Resources) > options.Limit {
		result.Resources = result.Resources[:options.Limit]
		result.Truncated = true
	}
	return result, nil
}

func ResolvePods(
	ctx context.Context,
	client kubernetes.Interface,
	resources []Resource,
) ([]corev1.Pod, error) {
	podsByID := make(map[string]corev1.Pod)
	for _, resource := range resources {
		if resource.Kind == "Pod" {
			if resource.pod == nil {
				return nil, fmt.Errorf("selected pod %s/%s is missing catalog data", resource.Namespace, resource.Name)
			}
			podsByID[resourceID(resource.pod)] = *resource.pod.DeepCopy()
			continue
		}
		if resource.podSelector == "" {
			return nil, fmt.Errorf("selected %s %s/%s has no pod selector", resource.Kind, resource.Namespace, resource.Name)
		}
		pods, err := client.CoreV1().Pods(resource.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: resource.podSelector,
		})
		if err != nil {
			return nil, fmt.Errorf("list pods for %s %s/%s: %w", resource.Kind, resource.Namespace, resource.Name, err)
		}
		for index := range pods.Items {
			pod := pods.Items[index]
			podsByID[resourceID(&pod)] = pod
		}
	}

	pods := make([]corev1.Pod, 0, len(podsByID))
	for _, pod := range podsByID {
		pods = append(pods, pod)
	}
	sort.Slice(pods, func(left, right int) bool {
		if pods[left].Namespace != pods[right].Namespace {
			return pods[left].Namespace < pods[right].Namespace
		}
		return pods[left].Name < pods[right].Name
	})
	return pods, nil
}

func resourceID(pod *corev1.Pod) string {
	if pod.UID != "" {
		return string(pod.UID)
	}
	return pod.Namespace + "/" + pod.Name
}

func listResources(ctx context.Context, client kubernetes.Interface) ([]Resource, error) {
	var resources []Resource
	pods, err := client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	for index := range pods.Items {
		pod := &pods.Items[index]
		resources = append(resources, resourceFromPod(pod))
	}
	deployments, err := client.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	for index := range deployments.Items {
		resources = append(resources, resourceFromDeployment(&deployments.Items[index]))
	}
	statefulSets, err := client.AppsV1().StatefulSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list statefulsets: %w", err)
	}
	for index := range statefulSets.Items {
		resources = append(resources, resourceFromStatefulSet(&statefulSets.Items[index]))
	}
	daemonSets, err := client.AppsV1().DaemonSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list daemonsets: %w", err)
	}
	for index := range daemonSets.Items {
		resources = append(resources, resourceFromDaemonSet(&daemonSets.Items[index]))
	}
	return resources, nil
}

func resourceFromPod(pod *corev1.Pod) Resource {
	return Resource{
		Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name, UID: string(pod.UID),
		Labels: maps.Clone(pod.Labels), pod: pod.DeepCopy(),
	}
}

func resourceFromDeployment(deployment *appsv1.Deployment) Resource {
	return Resource{
		Kind: "Deployment", Namespace: deployment.Namespace, Name: deployment.Name,
		UID: string(deployment.UID), Labels: maps.Clone(deployment.Labels),
		podSelector: metav1.FormatLabelSelector(deployment.Spec.Selector),
	}
}

func resourceFromStatefulSet(statefulSet *appsv1.StatefulSet) Resource {
	return Resource{
		Kind: "StatefulSet", Namespace: statefulSet.Namespace, Name: statefulSet.Name,
		UID: string(statefulSet.UID), Labels: maps.Clone(statefulSet.Labels),
		podSelector: metav1.FormatLabelSelector(statefulSet.Spec.Selector),
	}
}

func resourceFromDaemonSet(daemonSet *appsv1.DaemonSet) Resource {
	return Resource{
		Kind: "DaemonSet", Namespace: daemonSet.Namespace, Name: daemonSet.Name,
		UID: string(daemonSet.UID), Labels: maps.Clone(daemonSet.Labels),
		podSelector: metav1.FormatLabelSelector(daemonSet.Spec.Selector),
	}
}

func sortResources(resources []Resource) {
	sort.Slice(resources, func(left, right int) bool {
		a, b := resources[left], resources[right]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.UID < b.UID
	})
}
