package providers

import (
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/kubernetes/workload"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
)

func kubernetesTargetSelector(base string, filters []query.ColumnFilterValue) (workload.Selector, error) {
	selector, err := workload.ParseSelector(base)
	if err != nil {
		return workload.Selector{}, err
	}
	for _, filter := range filters {
		if !narrowsTarget(filter) {
			continue
		}
		narrowing, err := kubernetesFilterSelector(filter)
		if err != nil {
			return workload.Selector{}, fmt.Errorf("filter %q: %w", filter.Key, err)
		}
		selector = selector.And(narrowing)
	}
	return selector, nil
}

// narrowsTarget reports whether a filter narrows which workloads are read, as
// opposed to narrowing what is read from them.
//
// A pod pick is deliberately not a target narrowing. Selectors only ever AND,
// so `kind=Pod name=api-7f9` folded into a `kind=Deployment` scope matches no
// workload at all and would return an empty result for a pod that plainly
// exists; it narrows the pods the scope resolves to instead — see
// kubernetesPodSelectors.
func narrowsTarget(filter query.ColumnFilterValue) bool {
	switch filter.Kind {
	case query.ColumnFilterKindTime:
		return false
	case query.ColumnFilterKindWorkload:
		return !selectsPod(filter)
	default:
		return true
	}
}

func selectsPod(filter query.ColumnFilterValue) bool {
	if len(filter.Include) != 1 {
		return false
	}
	parts := strings.Split(filter.Include[0], "/")
	return len(parts) > 1 && strings.EqualFold(parts[len(parts)-2], "Pod")
}

// kubernetesPodSelectors is the pod narrowing the runtime filters imply, which
// applies to the pods the target scope resolved to rather than to the scope.
//
// It is deliberately kept apart from the profile's own `options.pods` rather
// than appended to it: ResourceSelectors match as a union, so one list holding
// both would widen the author's selection to include the picked pod instead of
// narrowing to it. The two are applied as separate gates.
func kubernetesPodSelectors(filters []query.ColumnFilterValue) (types.ResourceSelectors, error) {
	var selectors types.ResourceSelectors
	for _, filter := range filters {
		if filter.Kind != query.ColumnFilterKindWorkload || !selectsPod(filter) {
			continue
		}
		if len(filter.Exclude) > 0 {
			return nil, fmt.Errorf("filter %q: a workload selection excludes nothing", filter.Key)
		}
		parts := strings.Split(filter.Include[0], "/")
		selector := types.ResourceSelector{Name: parts[len(parts)-1]}
		if len(parts) == 3 {
			selector.Namespace = parts[0]
		}
		selectors = append(selectors, selector)
	}
	return selectors, nil
}

// kubernetesTimeRange is the bound the generated time control carries, in the
// date-math form it was written in — logs.LogsRequestBase resolves it, so
// resolving it here would only throw away "now-1h" and the intent with it.
func kubernetesTimeRange(filters []query.ColumnFilterValue) (start, end string) {
	for _, filter := range filters {
		if filter.Kind != query.ColumnFilterKindTime || filter.Range == nil {
			continue
		}
		if filter.Range.Min != nil {
			start = fmt.Sprint(filter.Range.Min.Value)
		}
		if filter.Range.Max != nil {
			end = fmt.Sprint(filter.Range.Max.Value)
		}
	}
	return start, end
}

func kubernetesFilterSelector(filter query.ColumnFilterValue) (workload.Selector, error) {
	switch {
	case filter.Kind == query.ColumnFilterKindWorkload:
		if len(filter.Include) != 1 || len(filter.Exclude) > 0 {
			return workload.Selector{}, fmt.Errorf("workload requires exactly one included value")
		}
		return workloadSelector(filter.Include[0])
	case filter.Kind == query.ColumnFilterKindLabels && filter.Field == "labels":
		return groupedLabelSelector(filter.Include, filter.Exclude)
	case strings.HasPrefix(filter.Field, "labels."):
		return labelValueSelector(filter.Field, filter.Include, filter.Exclude)
	default:
		return workload.Selector{}, fmt.Errorf("field %q is not a Kubernetes target filter", filter.Field)
	}
}

func workloadSelector(value string) (workload.Selector, error) {
	parts := strings.Split(value, "/")
	switch len(parts) {
	case 2:
		return workload.ParseSelector(fmt.Sprintf("kind=%s name=%s", parts[0], parts[1]))
	case 3:
		return workload.ParseSelector(fmt.Sprintf(
			"namespace=%s kind=%s name=%s", parts[0], parts[1], parts[2]))
	default:
		return workload.Selector{}, fmt.Errorf("workload %q must be [namespace/]kind/name", value)
	}
}

func groupedLabelSelector(include, exclude []string) (workload.Selector, error) {
	included := make(map[string][]string)
	excluded := make(map[string][]string)
	for _, entry := range include {
		key, value, _ := strings.Cut(entry, "=")
		included[key] = append(included[key], value)
	}
	for _, entry := range exclude {
		key, value, _ := strings.Cut(entry, "=")
		excluded[key] = append(excluded[key], value)
	}
	selector, err := workload.ParseSelector("")
	if err != nil {
		return workload.Selector{}, err
	}
	for key, values := range included {
		narrowing, err := labelValueSelector("labels."+key, values, nil)
		if err != nil {
			return workload.Selector{}, err
		}
		selector = selector.And(narrowing)
	}
	for key, values := range excluded {
		narrowing, err := labelValueSelector("labels."+key, nil, values)
		if err != nil {
			return workload.Selector{}, err
		}
		selector = selector.And(narrowing)
	}
	return selector, nil
}

func labelValueSelector(field string, include, exclude []string) (workload.Selector, error) {
	clauses := make([]string, 0, len(include)+len(exclude))
	if len(include) > 0 {
		values := make([]string, 0, len(include))
		for _, value := range include {
			values = append(values, field+"="+value)
		}
		clauses = append(clauses, "("+strings.Join(values, " | ")+")")
	}
	for _, value := range exclude {
		clauses = append(clauses, field+"!="+value)
	}
	return workload.ParseSelector(strings.Join(clauses, " "))
}
