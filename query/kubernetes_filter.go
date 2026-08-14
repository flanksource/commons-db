package query

import "github.com/flanksource/commons-db/kubernetes/workload"

const (
	kubernetesWorkloadFilterKey = "workload"
	kubernetesLabelsFilterKey   = "labels"
	kubernetesTimeFilterKey     = "time"

	// KubernetesDefaultTimeRange is how far back a Kubernetes log profile reads
	// when the request bounds nothing. An unbounded read is every line the
	// container still retains, from every pod in scope, buffered before the
	// first row is served — a default that cannot be what anyone meant.
	KubernetesDefaultTimeRange = ">=now-1h"
)

// RuntimeFilterBindings returns generated controls that narrow a provider's
// immutable profile query for one execution.
//
// The workload control is offered even when the profile query already names one
// exact target, because the choice it presents there is a different one: the
// lookup lists the pods that target resolves to, and reading one pod of a
// Deployment is the whole point of picking. Where the scope leaves a single
// option the browser renders it as a plain label rather than a picker, so an
// unnarrowable scope costs no control.
func (p Profile) RuntimeFilterBindings() ([]ColumnFilterBinding, error) {
	if p.Provider.Type != "k8s" {
		return nil, nil
	}
	if _, err := workload.ParseSelector(p.Query); err != nil {
		return nil, err
	}
	return []ColumnFilterBinding{
		{
			Key: kubernetesTimeFilterKey, Field: "timestamp", Label: "Time",
			Kind: ColumnFilterKindTime, Default: KubernetesDefaultTimeRange,
		},
		{
			Key: kubernetesWorkloadFilterKey, Field: kubernetesWorkloadFilterKey,
			Label: "Workload", Kind: ColumnFilterKindWorkload, Lookup: true,
		},
		{
			Key: kubernetesLabelsFilterKey, Field: kubernetesLabelsFilterKey,
			Label: "Labels", Kind: ColumnFilterKindLabels, Lookup: true, Multi: true,
		},
	}, nil
}

func (p Profile) allFilterBindings() ([]ColumnFilterBinding, error) {
	columns, err := p.ColumnFilterBindings()
	if err != nil {
		return nil, err
	}
	runtime, err := p.RuntimeFilterBindings()
	if err != nil {
		return nil, err
	}
	return append(append(columns, p.ParamFilterBindings()...), runtime...), nil
}

// FilterBindings returns every server-backed filter a profile exposes:
// rendered-column filters, bound parameters, and provider-generated runtime
// controls.
func (p Profile) FilterBindings() ([]ColumnFilterBinding, error) {
	return p.allFilterBindings()
}
