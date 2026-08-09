package providers

import (
	"fmt"
	"slices"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs/k8s"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
)

func init() {
	query.RegisterProvider(&k8sLogsProvider{})
}

// k8sLogsProvider reads pod logs straight from the Kubernetes API and returns
// one row per line, tagged with the pod, namespace and container it came from.
//
// It is the one log provider with no query language: there is nothing to send
// the kubelet but a workload to read from, so the whole request is structural
// and req.Query is unused.
type k8sLogsProvider struct{}

func (k8sLogsProvider) Type() string { return "k8s" }

// k8sLogKinds are the workloads logs can be read from. Anything else has no
// pods to resolve.
var k8sLogKinds = []string{"Pod", "Deployment", "StatefulSet", "DaemonSet"}

type k8sLogsOptions struct {
	// Kind is the workload to read logs from: Pod, Deployment, StatefulSet or
	// DaemonSet. A workload resolves to its current pods.
	Kind       string `json:"kind,omitempty"`
	ApiVersion string `json:"apiVersion,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name,omitempty"`

	// Pods narrows a workload's pods further, by name, label or namespace.
	Pods types.ResourceSelectors `json:"pods,omitempty"`

	// Containers picks which containers of each pod to read.
	Containers types.MatchExpressions `json:"containers,omitempty"`

	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
	Limit string `json:"limit,omitempty"`
}

func (k8sLogsProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	opts, err := query.DecodeOptions[k8sLogsOptions](req.Options)
	if err != nil {
		return nil, err
	}

	if !slices.Contains(k8sLogKinds, opts.Kind) {
		return nil, fmt.Errorf("k8s option `kind` is %q, want one of %v", opts.Kind, k8sLogKinds)
	}
	if opts.Namespace == "" || opts.Name == "" {
		return nil, fmt.Errorf("k8s requires the `namespace` and `name` options")
	}

	request := k8s.Request{
		Kind:       opts.Kind,
		ApiVersion: opts.ApiVersion,
		Namespace:  opts.Namespace,
		Name:       opts.Name,
		Pods:       opts.Pods,
		Containers: opts.Containers,
	}
	request.Start = opts.Start
	request.End = opts.End
	request.Limit = opts.Limit

	conn := connection.KubernetesConnection{
		KubeconfigConnection: connection.KubeconfigConnection{ConnectionName: req.Connection},
	}

	results, err := k8s.New(conn).Search(ctx, request)
	if err != nil {
		return nil, err
	}

	return logResultsToRows(results), nil
}
