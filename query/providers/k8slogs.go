package providers

import (
	"fmt"
	"iter"
	"sort"
	"time"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/kubernetes/workload"
	"github.com/flanksource/commons-db/logs/k8s"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
)

func init() {
	query.RegisterProvider(&k8sLogsProvider{})
}

// k8sLogsProvider reads logs from every Kubernetes workload matched by the
// profile's target selector. Runtime filters only narrow that immutable base.
type k8sLogsProvider struct{}

func (k8sLogsProvider) Type() string { return "k8s" }

func (k8sLogsProvider) PagingModes() query.PagingMode { return query.PagingOffset }

type k8sLogsOptions struct {
	// Pods narrows the pods resolved from the selected workloads.
	Pods types.ResourceSelectors `json:"pods,omitempty"`

	// Containers picks which containers of each pod to read.
	Containers types.MatchExpressions `json:"containers,omitempty"`

	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
	Limit string `json:"limit,omitempty"`
}

func (p k8sLogsProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	rows, _, err := p.execute(ctx, req)
	return rows, err
}

func (p k8sLogsProvider) Pages(
	ctx context.Context,
	req query.ProviderRequest,
	page query.PageRequest,
) iter.Seq2[query.Page, error] {
	return func(yield func(query.Page, error) bool) {
		rows, truncated, err := p.execute(ctx, req)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		total := query.Total{Value: int64(len(rows)), Exact: true}
		for start := min(page.Offset, len(rows)); ; start += page.Limit {
			end := min(start+page.Limit, len(rows))
			if !yield(query.Page{
				Rows: rows[start:end], HasMore: end < len(rows), Total: &total,
				Truncated: truncated,
			}, nil) || end >= len(rows) {
				return
			}
		}
	}
}

func (k8sLogsProvider) execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, bool, error) {
	for _, legacy := range []string{"kind", "apiVersion", "namespace", "name", "uid", "labels"} {
		if _, ok := req.Options[legacy]; ok {
			return nil, false, fmt.Errorf("k8s target option %q is unsupported; declare kind, namespace, name, uid, or labels.<key> in query", legacy)
		}
	}
	opts, err := query.DecodeOptions[k8sLogsOptions](req.Options)
	if err != nil {
		return nil, false, err
	}
	selector, err := kubernetesTargetSelector(req.Query, req.Filters)
	if err != nil {
		return nil, false, err
	}
	client, _, err := (connection.KubeconfigConnection{ConnectionName: req.Connection}).Populate(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("populate Kubernetes connection: %w", err)
	}
	limit, err := connection.KubernetesMaxResources(ctx, req.Connection)
	if err != nil {
		return nil, false, err
	}
	selected, err := workload.List(ctx, client, workload.ListOptions{Selector: selector, Limit: limit})
	if err != nil {
		return nil, false, err
	}
	pods, err := workload.ResolvePods(ctx, client, selected.Resources)
	if err != nil {
		return nil, false, err
	}
	podFilters, err := kubernetesPodSelectors(req.Filters)
	if err != nil {
		return nil, false, err
	}
	pods = k8s.SelectPods(pods, podFilters)
	request := k8s.Request{Pods: opts.Pods, Containers: opts.Containers}
	request.Start, request.End, request.Limit = opts.Start, opts.End, opts.Limit
	// The generated time control is the operator's answer and the option is the
	// author's, so a bound the request carries replaces the profile's default
	// rather than intersecting with it.
	if start, end := kubernetesTimeRange(req.Filters); start != "" || end != "" {
		request.Start, request.End = start, end
	}
	results, err := k8s.Fetch(ctx, client, pods, request)
	if err != nil {
		return nil, false, err
	}
	rows := logResultsToRows(results)
	sortLogRows(rows)
	return rows, selected.Truncated, nil
}

// NaturalOrder is the order this provider returns rows in, and the one that
// makes a page of them identifiable twice running.
//
// Logs have no natural key, so the tiebreaker is the id the fetch stamps on
// each line — pod, container and position within that container's stream. Two
// lines written in the same instant by different containers are ordered by it
// rather than by whichever pod's stream was read first.
func (k8sLogsProvider) NaturalOrder(query.ProviderConfig) (query.Order, error) {
	return query.Order{
		{Column: "timestamp", Desc: true},
		{Column: "id", Unique: true},
	}, nil
}

// sortLogRows puts the newest line first, which is the order the logs table
// presents and the order logs.dedupe reads its batches under — it takes the
// first row of a batch as the last observation.
func sortLogRows(rows []query.Row) {
	sort.SliceStable(rows, func(left, right int) bool {
		leftTime, rightTime := rowTimestamp(rows[left]), rowTimestamp(rows[right])
		if !leftTime.Equal(rightTime) {
			return leftTime.After(rightTime)
		}
		return rowString(rows[left], "id") < rowString(rows[right], "id")
	})
}

func rowTimestamp(row query.Row) time.Time {
	switch value := row["timestamp"].(type) {
	case time.Time:
		return value
	case *time.Time:
		if value != nil {
			return *value
		}
	}
	return time.Time{}
}

func rowString(row query.Row, key string) string {
	value, _ := row[key].(string)
	return value
}
