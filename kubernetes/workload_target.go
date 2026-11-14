package kubernetes

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// WorkloadTarget names a port on a pod-backed workload. Kind is "pod",
// "service" or a controller kind (deployment, statefulset, daemonset,
// replicaset). Name selects the object; without a Name, LabelSelector selects
// the pods directly (services for kind service). Port is a number, a port name
// (a Service port for kind service, else a container port) or empty for the
// first container port.
//
// Resolution needs get/list on pods, get on the named kind, and list on
// services when a service is selected by label.
type WorkloadTarget struct {
	Namespace     string
	Kind          string
	Name          string
	LabelSelector string
	Port          string
}

// PodPort is the Ready pod and numeric container port a WorkloadTarget resolved to.
type PodPort struct {
	Namespace string
	Pod       string
	Port      int
}

type podSelectorLookup func(ctx context.Context, k8s kubernetes.Interface, namespace, name string) (*metav1.LabelSelector, error)

// controllerKinds maps each controller kind to the lookup of its spec.selector.
var controllerKinds = map[string]podSelectorLookup{
	"deployment": func(ctx context.Context, k8s kubernetes.Interface, namespace, name string) (*metav1.LabelSelector, error) {
		o, err := k8s.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return o.Spec.Selector, nil
	},
	"statefulset": func(ctx context.Context, k8s kubernetes.Interface, namespace, name string) (*metav1.LabelSelector, error) {
		o, err := k8s.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return o.Spec.Selector, nil
	},
	"daemonset": func(ctx context.Context, k8s kubernetes.Interface, namespace, name string) (*metav1.LabelSelector, error) {
		o, err := k8s.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return o.Spec.Selector, nil
	},
	"replicaset": func(ctx context.Context, k8s kubernetes.Interface, namespace, name string) (*metav1.LabelSelector, error) {
		o, err := k8s.AppsV1().ReplicaSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return o.Spec.Selector, nil
	},
}

// WorkloadKinds lists every kind a WorkloadTarget accepts, sorted.
func WorkloadKinds() []string {
	return slices.Sorted(slices.Values(append(slices.Collect(maps.Keys(controllerKinds)), "pod", "service")))
}

func (t WorkloadTarget) validate() error {
	if !slices.Contains(WorkloadKinds(), t.Kind) {
		return fmt.Errorf("kind %q is not one of %s", t.Kind, strings.Join(WorkloadKinds(), ", "))
	}
	if t.Namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if t.Name == "" && t.LabelSelector == "" {
		return fmt.Errorf("either a name or a label selector is required")
	}
	return nil
}

func (t WorkloadTarget) String() string {
	ref := t.Name
	if ref == "" {
		ref = "[" + t.LabelSelector + "]"
	}
	return fmt.Sprintf("%s/%s in namespace %s", t.Kind, ref, t.Namespace)
}

// ResolvePodPort resolves target to one Ready pod, the first by name among the
// candidates, and the numeric container port it listens on.
func ResolvePodPort(ctx context.Context, k8s kubernetes.Interface, target WorkloadTarget) (PodPort, error) {
	if err := target.validate(); err != nil {
		return PodPort{}, err
	}
	if target.Kind == "service" {
		return resolveServicePod(ctx, k8s, target)
	}
	pod, err := readyWorkloadPod(ctx, k8s, target)
	if err != nil {
		return PodPort{}, fmt.Errorf("%s: %w", target, err)
	}
	port, err := containerPort(pod, target.Port)
	if err != nil {
		return PodPort{}, err
	}
	return PodPort{Namespace: target.Namespace, Pod: pod.Name, Port: port}, nil
}

func readyWorkloadPod(ctx context.Context, k8s kubernetes.Interface, target WorkloadTarget) (*corev1.Pod, error) {
	if target.Kind == "pod" && target.Name != "" {
		pod, err := k8s.CoreV1().Pods(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if !podReady(pod) {
			return nil, fmt.Errorf("pod %s is not running and ready", pod.Name)
		}
		return pod, nil
	}
	selector := target.LabelSelector
	if target.Name != "" {
		spec, err := controllerKinds[target.Kind](ctx, k8s, target.Namespace, target.Name)
		if err != nil {
			return nil, err
		}
		if selector, err = podSelector(spec); err != nil {
			return nil, err
		}
	}
	return listReadyPod(ctx, k8s, target.Namespace, selector)
}

// podSelector renders a controller's spec.selector, refusing an empty one that
// would match every pod in the namespace.
func podSelector(spec *metav1.LabelSelector) (string, error) {
	if spec == nil || (len(spec.MatchLabels) == 0 && len(spec.MatchExpressions) == 0) {
		return "", fmt.Errorf("has no pod selector")
	}
	selector, err := metav1.LabelSelectorAsSelector(spec)
	if err != nil {
		return "", fmt.Errorf("invalid pod selector: %w", err)
	}
	return selector.String(), nil
}

func listReadyPod(ctx context.Context, k8s kubernetes.Interface, namespace, selector string) (*corev1.Pod, error) {
	pods, err := k8s.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list pods %s: %w", selector, err)
	}
	return selectReadyPod(pods.Items)
}

func resolveServicePod(ctx context.Context, k8s kubernetes.Interface, target WorkloadTarget) (PodPort, error) {
	svc, err := findService(ctx, k8s, target)
	if err != nil {
		return PodPort{}, fmt.Errorf("%s: %w", target, err)
	}
	if len(svc.Spec.Selector) == 0 {
		return PodPort{}, fmt.Errorf("%s: service %s has no selector", target, svc.Name)
	}
	pod, err := listReadyPod(ctx, k8s, target.Namespace, labels.Set(svc.Spec.Selector).String())
	if err != nil {
		return PodPort{}, fmt.Errorf("%s: %w", target, err)
	}
	port, err := serviceTargetPort(svc, pod, target.Port)
	if err != nil {
		return PodPort{}, err
	}
	return PodPort{Namespace: target.Namespace, Pod: pod.Name, Port: port}, nil
}

func findService(ctx context.Context, k8s kubernetes.Interface, target WorkloadTarget) (*corev1.Service, error) {
	if target.Name != "" {
		return k8s.CoreV1().Services(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	}
	services, err := k8s.CoreV1().Services(target.Namespace).List(ctx, metav1.ListOptions{LabelSelector: target.LabelSelector})
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	if len(services.Items) == 0 {
		return nil, fmt.Errorf("no services match selector %s", target.LabelSelector)
	}
	sort.Slice(services.Items, func(i, j int) bool { return services.Items[i].Name < services.Items[j].Name })
	return &services.Items[0], nil
}

// containerPort resolves port (a number, a container port name, or empty for
// the first declared container port) on pod.
func containerPort(pod *corev1.Pod, port string) (int, error) {
	if n, err := strconv.Atoi(port); err == nil && n > 0 {
		return n, nil
	}
	for _, container := range pod.Spec.Containers {
		for _, p := range container.Ports {
			if port == "" || p.Name == port {
				return int(p.ContainerPort), nil
			}
		}
	}
	if port == "" {
		return 0, fmt.Errorf("pod %s declares no container ports and no port was given", pod.Name)
	}
	return 0, fmt.Errorf("pod %s has no container port named %q", pod.Name, port)
}
