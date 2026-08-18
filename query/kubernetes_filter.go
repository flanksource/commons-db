package query

import "github.com/flanksource/commons-db/kubernetes/workload"

const (
	kubernetesWorkloadFilterKey = "workload"
	kubernetesLabelsFilterKey   = "labels"
	kubernetesTimeFilterKey     = "time"

	// KubernetesDefaultStart is how far back a Kubernetes log profile reads when
	// nothing bounds it. An unbounded read is every line the container still
	// retains, from every pod in scope, buffered before the first row is served —
	// a default that cannot be what anyone meant.
	KubernetesDefaultStart = "now-1h"

	// KubernetesDefaultTimeRange is that same floor written in the filter grammar
	// the generated time control carries.
	KubernetesDefaultTimeRange = ">=" + KubernetesDefaultStart
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
//
// The time control is generated only for a profile that declares no time bound
// of its own. A profile carrying a time-from/time-to parameter pair already has
// one, and the provider reads that pair — generating a second control would put
// two pickers on one window, each writing a bound the other cannot see.
func (p Profile) RuntimeFilterBindings() ([]ColumnFilterBinding, error) {
	if p.Provider.Type != "k8s" {
		return nil, nil
	}
	if _, err := workload.ParseSelector(p.Query); err != nil {
		return nil, err
	}
	bindings := make([]ColumnFilterBinding, 0, 3)
	if !p.HasTimeRangeParams() {
		bindings = append(bindings, ColumnFilterBinding{
			Key: kubernetesTimeFilterKey, Field: "timestamp", Label: "Time",
			Kind: ColumnFilterKindTime, Default: KubernetesDefaultTimeRange,
		})
	}
	return append(bindings, []ColumnFilterBinding{
		{
			Key: kubernetesWorkloadFilterKey, Field: kubernetesWorkloadFilterKey,
			Label: "Workload", Kind: ColumnFilterKindWorkload, Lookup: true,
		},
		{
			Key: kubernetesLabelsFilterKey, Field: kubernetesLabelsFilterKey,
			Label: "Labels", Kind: ColumnFilterKindLabels, Lookup: true, Multi: true,
		},
	}...), nil
}

// TimeRangeParams returns the parameters a profile declares as its own time
// bound, keyed by role. Either edge may be absent — a profile that only bounds
// where a read starts is as valid as one that bounds both ends.
func (p Profile) TimeRangeParams() map[ParamRole]ParamDef {
	var params map[ParamRole]ParamDef
	for _, param := range p.Params {
		if param.Role != ParamRoleTimeFrom && param.Role != ParamRoleTimeTo {
			continue
		}
		if params == nil {
			params = make(map[ParamRole]ParamDef, 2)
		}
		params[param.Role] = param
	}
	return params
}

// HasTimeRangeParams reports whether the profile declares its own time bound.
func (p Profile) HasTimeRangeParams() bool {
	return len(p.TimeRangeParams()) > 0
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
