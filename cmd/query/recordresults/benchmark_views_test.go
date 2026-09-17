package recordresults_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

const (
	benchmarkViewJobs      = 100_000
	benchmarkViewBatchJobs = 10_000
)

// BenchmarkResultView reads the grouped views of a stream of 200,000 rows —
// a start and an end for each of 100,000 jobs — over the modernc driver: the
// first page of the per-job GROUP BY with its total, and a single-row
// overview that materializes it through uses.
func BenchmarkResultView(b *testing.B) {
	schemas := recordstore.NewSchemas()
	store, err := sqlite.Open(sqlite.Options{
		Path: filepath.Join(b.TempDir(), "records.sqlite"), Schema: schemas.Kind, TTL: benchmarkTTL, SweepInterval: benchmarkTTL,
	})
	if err != nil {
		b.Fatalf("open benchmark store: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })
	registry, err := recordresults.NewRegistry(recordresults.RegistryOptions{
		Prefix: "recordstore-benchmark", Schemas: schemas, Index: store, Source: store, ConnectionName: "index",
	})
	if err != nil {
		b.Fatalf("open benchmark registry: %v", err)
	}
	if err := recordresults.RegisterResultType(registry, recordresults.ResultType[jobEvent]{
		Kind: "job_event", Title: "Job events", Views: jobViews(),
	}); err != nil {
		b.Fatalf("register benchmark views: %v", err)
	}
	appendBenchmarkJobs(b, store)

	for _, view := range []struct {
		name   string
		params map[string]any
		rows   int
		total  int64
	}{
		{name: "jobs", params: map[string]any{"stream": benchmarkStream}, rows: benchmarkPageSize, total: benchmarkViewJobs},
		{name: "overview", params: map[string]any{"stream": benchmarkStream, "minMs": 500}, rows: 1, total: 1},
	} {
		b.Run(view.name, func(b *testing.B) {
			profile, err := registry.Get(context.Background(), "recordstore-benchmark/job_event/"+view.name)
			if err != nil {
				b.Fatalf("get view profile: %v", err)
			}
			read := benchmarkQuery{registry: registry, profile: profile, index: store}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				page, err := readBenchmarkPage(read, query.PageRequest{Limit: benchmarkPageSize}, view.params)
				if err != nil {
					b.Fatalf("read view %s: %v", view.name, err)
				}
				if len(page.Rows) != view.rows || page.Total == nil || page.Total.Value != view.total {
					b.Fatalf("view %s returned %d rows of %v, want %d of %d", view.name, len(page.Rows), page.Total, view.rows, view.total)
				}
			}
		})
	}
}

// appendBenchmarkJobs appends a start then an end for each benchmark job, job
// n taking n%1000 milliseconds.
func appendBenchmarkJobs(b *testing.B, store *sqlite.Backend) {
	for first := 0; first < benchmarkViewJobs; first += benchmarkViewBatchJobs {
		events := make([]jobEvent, 0, 2*benchmarkViewBatchJobs)
		for n := first; n < first+benchmarkViewBatchJobs; n++ {
			job := fmt.Sprintf("job-%06d", n)
			events = append(events,
				jobEvent{ID: job + ":start", Job: job, Phase: "start"},
				jobEvent{ID: job + ":end", Job: job, Phase: "end", Status: "ok", Millis: float64(n % 1000)},
			)
		}
		if _, err := recordstore.AppendTyped(context.Background(), store, benchmarkStream, "job_event", events); err != nil {
			b.Fatalf("append benchmark jobs: %v", err)
		}
	}
}
