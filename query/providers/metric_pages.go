package providers

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers/sqlite"
	"github.com/google/uuid"
)

type metricSnapshot struct {
	path, scope, statement string
	metadata               *query.MetricMetadata
	leases                 int
	timer                  *time.Timer
}

var metricSnapshots = struct {
	sync.Mutex
	items map[string]*metricSnapshot
}{items: map[string]*metricSnapshot{}}

func (prometheusProvider) PagingModes() query.PagingMode {
	return query.PagingOffset | query.PagingCursor
}
func (cloudwatchMetricsProvider) PagingModes() query.PagingMode {
	return query.PagingOffset | query.PagingCursor
}
func (prometheusProvider) SupportsRequestSort() bool        { return true }
func (cloudwatchMetricsProvider) SupportsRequestSort() bool { return true }
func (p prometheusProvider) Pages(ctx context.Context, req query.ProviderRequest, page query.PageRequest) iter.Seq2[query.Page, error] {
	return metricPages(ctx, req, page, p.Execute)
}
func (p cloudwatchMetricsProvider) Pages(ctx context.Context, req query.ProviderRequest, page query.PageRequest) iter.Seq2[query.Page, error] {
	return metricPages(ctx, req, page, p.Execute)
}

func metricPages(ctx context.Context, req query.ProviderRequest, page query.PageRequest, execute func(context.Context, query.ProviderRequest) ([]query.Row, error)) iter.Seq2[query.Page, error] {
	return func(yield func(query.Page, error) bool) {
		if page.Mode() == query.PagingCursor {
			if err := req.Order.Pageable(); err != nil {
				yield(query.Page{}, err)
				return
			}
		}
		scope, err := metricScope(ctx, req)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		id := req.Position.PIT
		var item *metricSnapshot
		if id == "" {
			rows, err := execute(ctx, req)
			if err != nil {
				yield(query.Page{}, err)
				return
			}
			if len(rows) == 0 {
				yield(query.Page{Rows: rows, Total: &query.Total{Exact: true}, Metrics: req.MetricMetadata}, nil)
				return
			}
			id, item, err = createMetricSnapshot(ctx, scope, rows, req.MetricMetadata)
			if err != nil {
				yield(query.Page{}, err)
				return
			}
		}
		item, release, err := acquireMetricSnapshot(id, scope)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		defer release()
		request := req
		request.Connection = ""
		request.Options = map[string]any{"url": "file:" + filepath.ToSlash(item.path) + "?mode=ro"}
		request.Query = item.statement
		request.QueryArgs = nil
		request.QueryIdentifiers = nil
		request.Position.PIT = ""
		provider := sqlite.NewSQLProvider("sqlite", "sqlite").(query.PagingProvider)
		for result, err := range provider.Pages(ctx, request, page) {
			result.PIT = id
			result.Metrics = item.metadata
			if !yield(result, err) || err != nil {
				return
			}
		}
	}
}

func metricScope(ctx context.Context, req query.ProviderRequest) (string, error) {
	b, err := json.Marshal([]any{ctx.Subject(), ctx.ConnectionCacheScope(), req.Provider, req.Connection, req.Query, req.QueryArgs, req.Options, req.Labels, req.Order, req.Filters})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}
func createMetricSnapshot(ctx context.Context, scope string, rows []query.Row, metadata *query.MetricMetadata) (string, *metricSnapshot, error) {
	dir := filepath.Join(".tmp", "metric-results")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", nil, err
	}
	id := uuid.NewString()
	path := filepath.Join(dir, id+".sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", nil, err
	}
	defer db.Close()
	keys := []string{}
	for k := range rows[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	columns := make([]query.ColumnDef, len(keys))
	for i, k := range keys {
		typ := query.ColumnTypeString
		for _, r := range rows {
			if r[k] != nil {
				switch r[k].(type) {
				case int64, float64:
					typ = query.ColumnTypeNumber
				case bool:
					typ = query.ColumnTypeBoolean
				}
				break
			}
		}
		columns[i] = query.ColumnDef{Name: k, Type: typ}
	}
	table, err := sqlitetable.Write(ctx, db, sqlitetable.Table{Name: "metric_results", Columns: columns}, rows)
	if err != nil {
		os.Remove(path)
		return "", nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		os.Remove(path)
		return "", nil, err
	}
	projection := make([]string, len(keys))
	for i, k := range keys {
		physical, err := table.Physical(k)
		if err != nil {
			return "", nil, err
		}
		projection[i] = physical
		for _, r := range rows {
			if r[k] != nil {
				if _, ok := r[k].(float64); ok {
					projection[i] = "CAST(" + physical + " AS REAL)"
				}
				break
			}
		}
		projection[i] += " AS " + sqlitetable.QuoteIdentifier(k)
	}
	item := &metricSnapshot{path: path, scope: scope, statement: "SELECT " + strings.Join(projection, ",") + " FROM metric_results", metadata: metadata}
	metricSnapshots.Lock()
	metricSnapshots.items[id] = item
	scheduleMetricExpiry(id, item)
	metricSnapshots.Unlock()
	return id, item, nil
}
func acquireMetricSnapshot(id, scope string) (*metricSnapshot, func(), error) {
	metricSnapshots.Lock()
	defer metricSnapshots.Unlock()
	item := metricSnapshots.items[id]
	if item == nil {
		return nil, nil, fmt.Errorf("metric result expired; rerun the profile")
	}
	if item.scope != scope {
		return nil, nil, fmt.Errorf("metric result does not belong to this query or caller")
	}
	item.timer.Stop()
	item.leases++
	var once sync.Once
	return item, func() {
		once.Do(func() {
			metricSnapshots.Lock()
			defer metricSnapshots.Unlock()
			item.leases--
			if item.leases == 0 {
				scheduleMetricExpiry(id, item)
			}
		})
	}, nil
}
func scheduleMetricExpiry(id string, item *metricSnapshot) {
	item.timer = time.AfterFunc(5*time.Minute, func() {
		metricSnapshots.Lock()
		defer metricSnapshots.Unlock()
		if item.leases == 0 {
			delete(metricSnapshots.items, id)
			os.Remove(item.path)
		}
	})
}
