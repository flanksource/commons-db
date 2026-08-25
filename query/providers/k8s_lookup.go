package providers

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/client-go/kubernetes"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/kubernetes/workload"
	"github.com/flanksource/commons-db/query"
)

func (k8sLogsProvider) LookupFilterValues(
	ctx context.Context,
	req query.ProviderRequest,
	binding query.ColumnFilterBinding,
	search string,
	limit int,
) ([]query.FilterOption, *query.Total, error) {
	selector, err := kubernetesTargetSelector(req.Query, req.Filters)
	if err != nil {
		return nil, nil, err
	}
	client, _, err := (connection.KubeconfigConnection{ConnectionName: req.Connection}).Populate(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("populate Kubernetes connection: %w", err)
	}
	// The same cap an execution reads under. Offering a workload the query would
	// refuse to read is a choice that answers with nothing.
	max, err := connection.KubernetesMaxResources(ctx, req.Connection)
	if err != nil {
		return nil, nil, err
	}
	selected, err := workload.List(ctx, client, workload.ListOptions{Selector: selector, Limit: max})
	if err != nil {
		return nil, nil, err
	}
	resources := selected.Resources
	// A workload pick is how one pod of a Deployment is read, so the pods the
	// scope resolves to are options in their own right — otherwise a profile
	// naming one workload offers only the workload it already names.
	if binding.Kind == query.ColumnFilterKindWorkload {
		children, err := kubernetesChildPods(ctx, client, resources)
		if err != nil {
			return nil, nil, err
		}
		resources = append(resources, children...)
	}
	counts, err := kubernetesFilterOptionCounts(binding, resources)
	if err != nil {
		return nil, nil, err
	}
	options := make([]query.FilterOption, 0, len(counts))
	for value, count := range counts {
		if search == "" || strings.Contains(strings.ToLower(value), strings.ToLower(search)) {
			options = append(options, query.FilterOption{Value: value, Count: count})
		}
	}
	sort.Slice(options, func(left, right int) bool { return options[left].Value < options[right].Value })
	total := &query.Total{Value: int64(len(options)), Exact: true}
	if limit > 0 && len(options) > limit {
		options = options[:limit]
	}
	return options, total, nil
}

// kubernetesChildPods lists the pods the selected controllers resolve to, minus
// the pods already selected in their own right — a scope matching both a
// Deployment and one of its pods must not offer that pod twice.
func kubernetesChildPods(
	ctx context.Context,
	client kubernetes.Interface,
	resources []workload.Resource,
) ([]workload.Resource, error) {
	controllers := make([]workload.Resource, 0, len(resources))
	selected := make(map[string]bool, len(resources))
	for _, resource := range resources {
		if resource.Kind == "Pod" {
			selected[resource.Namespace+"/"+resource.Name] = true
			continue
		}
		controllers = append(controllers, resource)
	}
	if len(controllers) == 0 {
		return nil, nil
	}
	pods, err := workload.ResolvePods(ctx, client, controllers)
	if err != nil {
		return nil, err
	}
	children := make([]workload.Resource, 0, len(pods))
	for _, pod := range pods {
		if selected[pod.Namespace+"/"+pod.Name] {
			continue
		}
		children = append(children, workload.Resource{
			Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name,
			UID: string(pod.UID), Labels: pod.Labels,
		})
	}
	return children, nil
}

func kubernetesFilterOptionCounts(
	binding query.ColumnFilterBinding,
	resources []workload.Resource,
) (map[string]int64, error) {
	counts := make(map[string]int64)
	switch {
	case binding.Kind == query.ColumnFilterKindWorkload:
		for _, resource := range resources {
			counts[resource.Namespace+"/"+resource.Kind+"/"+resource.Name]++
		}
	case binding.Kind == query.ColumnFilterKindLabels && binding.Field == "labels":
		for _, resource := range resources {
			for key, value := range resource.Labels {
				counts[key+"="+value]++
			}
		}
	case strings.HasPrefix(binding.Field, "labels."):
		key := strings.TrimPrefix(binding.Field, "labels.")
		for _, resource := range resources {
			if value, ok := resource.Labels[key]; ok {
				counts[value]++
			}
		}
	default:
		return nil, fmt.Errorf("filter field %q has no Kubernetes lookup", binding.Field)
	}
	return counts, nil
}
