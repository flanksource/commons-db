package providers

import (
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"

	"github.com/flanksource/commons-db/logs/k8s"
)

const kubernetesDiagnosticListLimit = 20

func kubernetesLogRequestDetails(pods []corev1.Pod, request k8s.Request) map[string]any {
	pods = k8s.SelectPods(pods, request.Pods)
	podNames := make([]string, 0, len(pods))
	namespaceSet := make(map[string]struct{})
	for _, pod := range pods {
		podNames = append(podNames, fmt.Sprintf("%s/%s", pod.Namespace, pod.Name))
		namespaceSet[pod.Namespace] = struct{}{}
	}
	namespaces := make([]string, 0, len(namespaceSet))
	for namespace := range namespaceSet {
		namespaces = append(namespaces, namespace)
	}

	details := map[string]any{"pod_count": len(podNames)}
	boundDiagnosticList(details, "namespaces", namespaces)
	boundDiagnosticList(details, "pods", podNames)
	if len(request.Containers) > 0 {
		containers := make([]string, len(request.Containers))
		for index, expression := range request.Containers {
			containers[index] = string(expression)
		}
		boundDiagnosticList(details, "containers", containers)
	}
	for key, value := range map[string]string{
		"start": request.Start,
		"end":   request.End,
		"limit": request.Limit,
	} {
		if value != "" {
			details[key] = value
		}
	}
	return details
}

func boundDiagnosticList(details map[string]any, key string, values []string) {
	sort.Strings(values)
	if len(values) > kubernetesDiagnosticListLimit {
		details[key] = values[:kubernetesDiagnosticListLimit]
		details[key+"_truncated"] = true
		return
	}
	details[key] = values
}
