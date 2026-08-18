package providers

import (
	"fmt"
	"iter"
	"sort"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

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

// PagingModes exposes only the strategy the Kubernetes API can serve without
// re-reading the whole window. It has no offset or continuation token; the
// provider resumes a forward read from SinceTime and its own line position.
func (k8sLogsProvider) PagingModes() query.PagingMode {
	return query.PagingCursor
}

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
	if page.Mode() != query.PagingCursor {
		return query.ErrorPage(fmt.Errorf("Kubernetes logs page only by cursor, got %s", page.Mode()))
	}
	return p.cursorPages(ctx, req, page)
}

// cursorPages walks forward from the position the previous page ended at.
//
// Each page moves SinceTime up to the cursor's second and reads at most
// Limit+1 lines per container. That bound is sound because every container's
// own stream is ascending: the globally first Limit+1 lines after a position
// cannot include a line that is not among the first Limit+1 of its own stream.
// So a page costs the same however deep the walk has gone, unlike buffering
// the whole query window before slicing a page.
func (p k8sLogsProvider) cursorPages(
	ctx context.Context,
	req query.ProviderRequest,
	page query.PageRequest,
) iter.Seq2[query.Page, error] {
	return func(yield func(query.Page, error) bool) {
		if err := assertAscendingLogOrder(req.Order); err != nil {
			yield(query.Page{}, err)
			return
		}
		target, err := p.resolve(ctx, req)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		after, err := logPositionFromKeys(req.Position.Keys)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		remaining := page.Ceiling
		for {
			limit := page.Limit
			if remaining > 0 && remaining < limit {
				limit = remaining
			}
			request := target.request
			request.After = after
			request.Limit = strconv.Itoa(limit)
			if after != nil {
				// SinceTime names whole seconds, so the read restarts at the top
				// of the cursor's second and After discards the part of it that
				// was already served.
				request.Start = after.Timestamp.Truncate(time.Second).Format(time.RFC3339)
			}
			rows, err := target.read(ctx, request)
			if err != nil {
				yield(query.Page{}, err)
				return
			}
			more := len(rows) > limit
			if more {
				rows = rows[:limit]
			}
			capped := remaining > 0 && len(rows) >= remaining && more
			result := query.Page{
				Rows: rows, HasMore: more && !capped,
				Truncated: target.truncated || capped,
			}
			if result.HasMore && len(rows) > 0 {
				keys, err := orderKeys(rows[len(rows)-1], req.Order)
				if err != nil {
					yield(query.Page{}, err)
					return
				}
				result.NextKeys = keys
				if after, err = logPositionFromKeys(keys); err != nil {
					yield(query.Page{}, err)
					return
				}
			}
			if !yield(result, nil) || !result.HasMore {
				return
			}
			if remaining > 0 {
				remaining -= len(rows)
			}
		}
	}
}

// assertAscendingLogOrder refuses a cursor walk under an order this provider
// cannot resume. The kubelet resumes forward from an instant and nothing else,
// so a descending or differently-keyed order would page by re-reading the whole
// window and quietly returning the wrong rows.
func assertAscendingLogOrder(order query.Order) error {
	if len(order) == 2 &&
		order[0].Column == "timestamp" && !order[0].Desc &&
		order[1].Column == "id" {
		return nil
	}
	return fmt.Errorf(
		"a Kubernetes log cursor resumes from a timestamp the API can seek to, so it pages only by `timestamp` ascending then `id`; this profile orders by %v",
		order.Columns())
}

// logPositionFromKeys reads a decoded cursor back into the position the fetch
// resumes past.
func logPositionFromKeys(keys []any) (*k8s.Position, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	if len(keys) != 2 {
		return nil, fmt.Errorf("a Kubernetes log cursor carries a timestamp and an id, got %d keys", len(keys))
	}
	timestamp, ok := keys[0].(time.Time)
	if !ok {
		return nil, fmt.Errorf("cursor timestamp is %T, not a time", keys[0])
	}
	id, ok := keys[1].(string)
	if !ok {
		return nil, fmt.Errorf("cursor id is %T, not a string", keys[1])
	}
	return &k8s.Position{Timestamp: timestamp, ID: id}, nil
}

// k8sLogTarget is everything one execution resolved before reading a single
// line: which pods to read and under what bounds. A cursor walk resolves it once
// and re-reads only the log streams, so paging does not re-list the cluster on
// every page.
type k8sLogTarget struct {
	client      kubernetes.Interface
	pods        []corev1.Pod
	request     k8s.Request
	query       string
	diagnostics *query.ProviderDiagnostics
	truncated   bool
}

func (k8sLogsProvider) resolve(ctx context.Context, req query.ProviderRequest) (*k8sLogTarget, error) {
	for _, legacy := range []string{"kind", "apiVersion", "namespace", "name", "uid", "labels"} {
		if _, ok := req.Options[legacy]; ok {
			return nil, fmt.Errorf("k8s target option %q is unsupported; declare kind, namespace, name, uid, or labels.<key> in query", legacy)
		}
	}
	opts, err := query.DecodeOptions[k8sLogsOptions](req.Options)
	if err != nil {
		return nil, err
	}
	selector, err := kubernetesTargetSelector(req.Query, req.Filters)
	if err != nil {
		return nil, err
	}
	client, _, err := (connection.KubeconfigConnection{ConnectionName: req.Connection}).Populate(ctx)
	if err != nil {
		return nil, fmt.Errorf("populate Kubernetes connection: %w", err)
	}
	limit, err := connection.KubernetesMaxResources(ctx, req.Connection)
	if err != nil {
		return nil, err
	}
	selected, err := workload.List(ctx, client, workload.ListOptions{Selector: selector, Limit: limit})
	if err != nil {
		return nil, err
	}
	pods, err := workload.ResolvePods(ctx, client, selected.Resources)
	if err != nil {
		return nil, err
	}
	podFilters, err := kubernetesPodSelectors(req.Filters)
	if err != nil {
		return nil, err
	}
	pods = k8s.SelectPods(pods, podFilters)
	request := k8s.Request{Pods: opts.Pods, Containers: opts.Containers}
	request.Start, request.End, request.Limit = opts.Start, opts.End, opts.Limit
	// The time control is the operator's answer and the option is the author's, so
	// a bound the request carries replaces the profile's default rather than
	// intersecting with it.
	if start, end := kubernetesTimeRange(req); start != "" || end != "" {
		request.Start, request.End = start, end
	}
	// A read is always bounded below, whichever control bounded it: an upper edge
	// on its own, or a declared parameter pair nobody filled in, would otherwise
	// buffer every line every pod in scope still retains before serving a row.
	if request.Start == "" {
		request.Start = query.KubernetesDefaultStart
	}
	return &k8sLogTarget{
		client: client, pods: pods, request: request, query: req.Query,
		diagnostics: req.Diagnostics, truncated: selected.Truncated,
	}, nil
}

// read fetches the target's log lines under the request it carries and returns
// them in the ascending order this provider declares.
func (t *k8sLogTarget) read(ctx context.Context, request k8s.Request) ([]query.Row, error) {
	t.diagnostics.RecordRequest(t.query, nil, kubernetesLogRequestDetails(t.pods, request))
	results, err := k8s.Fetch(ctx, t.client, t.pods, request)
	if err != nil {
		return nil, err
	}
	rows := logResultsToRows(results)
	sortLogRows(rows)
	return rows, nil
}

func (p k8sLogsProvider) execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, bool, error) {
	target, err := p.resolve(ctx, req)
	if err != nil {
		return nil, false, err
	}
	rows, err := target.read(ctx, target.request)
	if err != nil {
		return nil, false, err
	}
	return rows, target.truncated, nil
}

// NaturalOrder is the order this provider returns rows in, and the one that
// makes a page of them identifiable twice running.
//
// It is ascending because that is the only direction the Kubernetes API can
// resume in: SinceTime is a lower bound, and there is no upper bound, no offset
// and no continuation token. A newest-first walk would have to re-read the whole
// window for every page and discard its newer half, so the direction is not a
// presentation choice here — it is what paging at all requires.
//
// Logs have no natural key, so the tiebreaker is the id the fetch stamps on each
// line — pod, container and position within its instant. Two lines written in
// the same instant by different containers are ordered by it rather than by
// whichever pod's stream was read first.
func (k8sLogsProvider) NaturalOrder(query.ProviderConfig) (query.Order, error) {
	return query.Order{
		{Column: "timestamp"},
		{Column: "id", Unique: true},
	}, nil
}

// sortLogRows merges the per-container streams into the ascending order the
// provider declares. Each stream is already ascending; this only interleaves
// them.
func sortLogRows(rows []query.Row) {
	sort.SliceStable(rows, func(left, right int) bool {
		leftTime, rightTime := rowTimestamp(rows[left]), rowTimestamp(rows[right])
		if !leftTime.Equal(rightTime) {
			return leftTime.Before(rightTime)
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
