package recordresults_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	_ "github.com/flanksource/commons-db/query/providers"
	"github.com/flanksource/commons-db/recordstore"
)

const benchmarkPageSize = 100

func BenchmarkRecordStoreAppend(b *testing.B) {
	for _, backend := range benchmarkBackends {
		for _, benchmarkCase := range benchmarkCases() {
			b.Run(string(backend)+"/"+benchmarkCase.name(), func(b *testing.B) {
				rows := benchmarkRows(benchmarkCase)
				b.ReportAllocs()
				b.SetBytes(benchmarkCase.datasetBytes())
				b.ResetTimer()
				for range b.N {
					b.StopTimer()
					schemas := recordstore.NewSchemas()
					registerBenchmarkSchema(b, schemas)
					source := openBenchmarkSource(b, backend, schemas)
					b.StartTimer()

					result, err := source.backend.Append(context.Background(), benchmarkStream, benchmarkKind, rows)

					b.StopTimer()
					if err != nil {
						source.Close(b)
						b.Fatalf("append benchmark records: %v", err)
					}
					if result.Window != (recordstore.Window{From: 1, To: int64(benchmarkCase.records)}) || result.Skipped != 0 {
						source.Close(b)
						b.Fatalf("append result = %+v, want rows 1-%d with none skipped", result, benchmarkCase.records)
					}
					meta, err := source.backend.Meta(context.Background(), benchmarkStream)
					if err != nil || meta.Total != int64(benchmarkCase.records) {
						source.Close(b)
						b.Fatalf("appended stream metadata = %+v, %v", meta, err)
					}
					source.Close(b)
				}
				reportBenchmarkRates(b, int64(benchmarkCase.records)*int64(b.N), benchmarkCase.datasetBytes()*int64(b.N), "records/s", "MiB/s")
			})
		}
	}
}

func BenchmarkRecordStoreRead(b *testing.B) {
	for _, backend := range benchmarkBackends {
		for _, benchmarkCase := range benchmarkCases() {
			b.Run(string(backend)+"/"+benchmarkCase.name(), func(b *testing.B) {
				rows := benchmarkRows(benchmarkCase)
				schemas := recordstore.NewSchemas()
				source := openBenchmarkSource(b, backend, schemas)
				queryBenchmark := openBenchmarkQuery(b, source, schemas)
				appendBenchmarkRows(b, source.backend, rows, benchmarkCase.records)

				b.Run("scan", func(b *testing.B) {
					benchmarkScan(b, source.backend, benchmarkCase)
				})
				b.Run("query-cold-first-page", func(b *testing.B) {
					queryBenchmark = benchmarkColdQuery(b, backend, source, schemas, queryBenchmark, benchmarkCase)
				})
				benchmarkWarmQueries(b, queryBenchmark, benchmarkCase)

				queryBenchmark.Close(b)
				source.Close(b)
			})
		}
	}
}

func appendBenchmarkRows(tb benchmarkTB, backend recordstore.Backend, rows []recordstore.Row, count int) {
	tb.Helper()
	result, err := backend.Append(context.Background(), benchmarkStream, benchmarkKind, rows)
	if err != nil {
		tb.Fatalf("seed benchmark stream: %v", err)
	}
	if result.Window != (recordstore.Window{From: 1, To: int64(count)}) || result.Skipped != 0 {
		tb.Fatalf("seed append result = %+v, want rows 1-%d with none skipped", result, count)
	}
}

func benchmarkScan(b *testing.B, backend recordstore.Backend, benchmarkCase benchmarkCase) {
	b.ReportAllocs()
	b.SetBytes(benchmarkCase.datasetBytes())
	var totalRows int64
	b.ResetTimer()
	for range b.N {
		rows := 0
		err := backend.Scan(context.Background(), benchmarkStream, 0, func(seq int64, row recordstore.Row) error {
			rows++
			if seq != int64(rows) || row["payload"] == nil {
				return fmt.Errorf("row %d has seq %d or no payload", rows, seq)
			}
			return nil
		})
		if err != nil {
			b.Fatalf("scan benchmark stream: %v", err)
		}
		if rows != benchmarkCase.records {
			b.Fatalf("scan returned %d rows, want %d", rows, benchmarkCase.records)
		}
		totalRows += int64(rows)
	}
	b.StopTimer()
	reportBenchmarkRates(b, totalRows, int64(benchmarkCase.recordBytes)*totalRows, "records/s", "MiB/s")
}

func benchmarkColdQuery(
	b *testing.B,
	backend benchmarkBackend,
	source benchmarkSource,
	schemas *recordstore.Schemas,
	initial benchmarkQuery,
	benchmarkCase benchmarkCase,
) benchmarkQuery {
	b.ReportAllocs()
	var returned int64
	b.ResetTimer()
	for iteration := range b.N {
		current := initial
		if iteration > 0 {
			b.StopTimer()
			current = openBenchmarkQuery(b, source, schemas)
			b.StartTimer()
		}
		page, err := readBenchmarkPage(current, query.PageRequest{Limit: benchmarkPageSize}, benchmarkParams())
		if err != nil {
			b.Fatalf("cold benchmark query: %v", err)
		}
		if len(page.Rows) != min(benchmarkPageSize, benchmarkCase.records) {
			b.Fatalf("cold query returned %d rows, want %d", len(page.Rows), min(benchmarkPageSize, benchmarkCase.records))
		}
		returned += int64(len(page.Rows))
		if iteration > 0 {
			b.StopTimer()
			current.Close(b)
			b.StartTimer()
		}
	}
	b.StopTimer()
	reportBenchmarkRates(b, returned, int64(benchmarkCase.recordBytes)*returned, "rows/s", "result-MiB/s")
	if backend != benchmarkSQLite {
		reportBenchmarkRates(
			b,
			int64(benchmarkCase.records)*int64(b.N),
			benchmarkCase.datasetBytes()*int64(b.N),
			"indexed-records/s",
			"indexed-MiB/s",
		)
	}
	return initial
}

type benchmarkQueryCase struct {
	name     string
	request  query.PageRequest
	params   map[string]any
	expected int
}

func benchmarkWarmQueries(b *testing.B, queryBenchmark benchmarkQuery, benchmarkCase benchmarkCase) {
	lastOffset := max(benchmarkCase.records-benchmarkPageSize, 0)
	cursorPage, err := readBenchmarkPage(queryBenchmark, query.PageRequest{
		Limit: benchmarkPageSize, Offset: max(benchmarkCase.records-2*benchmarkPageSize, 0),
	}, benchmarkParams())
	if err != nil {
		b.Fatalf("prepare final-page cursor: %v", err)
	}
	if cursorPage.Next.IsZero() {
		b.Fatal("prepare final-page cursor: penultimate page has no next cursor")
	}
	rangeStart := 3 * benchmarkCase.records / 4
	queryCases := []benchmarkQueryCase{
		{name: "page-first-offset", request: query.PageRequest{Limit: benchmarkPageSize}, params: benchmarkParams(), expected: benchmarkPageSize},
		{name: "page-last-offset", request: query.PageRequest{Limit: benchmarkPageSize, Offset: lastOffset}, params: benchmarkParams(), expected: benchmarkPageSize},
		{name: "page-last-cursor", request: query.PageRequest{Limit: benchmarkPageSize, Cursor: cursorPage.Next}, params: benchmarkParams(), expected: min(benchmarkPageSize, benchmarkCase.records-benchmarkPageSize)},
		{name: "filter-terms", request: query.PageRequest{Limit: benchmarkPageSize}, params: benchmarkParams("filter.group", "group-01"), expected: min(benchmarkPageSize, benchmarkCase.records/8)},
		{name: "filter-range", request: query.PageRequest{Limit: benchmarkPageSize}, params: benchmarkParams("filter.ordinal", fmt.Sprintf(">=%d", rangeStart)), expected: min(benchmarkPageSize, benchmarkCase.records-rangeStart+1)},
		{name: "filter-array", request: query.PageRequest{Limit: benchmarkPageSize}, params: benchmarkParams("filter.tags", "even"), expected: min(benchmarkPageSize, benchmarkCase.records/2)},
		{name: "sort-group", request: query.PageRequest{Limit: benchmarkPageSize, Sort: "group", Desc: true}, params: benchmarkParams(), expected: benchmarkPageSize},
	}
	for _, queryCase := range queryCases {
		b.Run("query-warm/"+queryCase.name, func(b *testing.B) {
			b.ReportAllocs()
			var returned int64
			b.ResetTimer()
			for range b.N {
				page, err := readBenchmarkPage(queryBenchmark, queryCase.request, queryCase.params)
				if err != nil {
					b.Fatalf("warm benchmark query %s: %v", queryCase.name, err)
				}
				if len(page.Rows) != queryCase.expected {
					b.Fatalf("warm query %s returned %d rows, want %d", queryCase.name, len(page.Rows), queryCase.expected)
				}
				returned += int64(len(page.Rows))
			}
			b.StopTimer()
			reportBenchmarkRates(b, returned, int64(benchmarkCase.recordBytes)*returned, "rows/s", "result-MiB/s")
		})
	}
}

func benchmarkParams(values ...any) map[string]any {
	params := map[string]any{"stream": benchmarkStream}
	for index := 0; index < len(values); index += 2 {
		params[values[index].(string)] = values[index+1]
	}
	return params
}

func readBenchmarkPage(
	queryBenchmark benchmarkQuery,
	request query.PageRequest,
	params map[string]any,
) (query.Page, error) {
	ctx := dbcontext.New().WithConnectionResolver(queryBenchmark.registry.ResolveConnection)
	release, err := queryBenchmark.registry.BeforeExecute(ctx, []profiles.ReadRequest{{
		Profile: queryBenchmark.profile,
		Params:  params,
	}})
	if err != nil {
		return query.Page{}, err
	}
	defer release()
	for page, err := range query.ExecutePages(ctx, queryBenchmark.profile, request, params) {
		return page, err
	}
	return query.Page{}, fmt.Errorf("profile %q returned no page", queryBenchmark.profile.Name)
}

func reportBenchmarkRates(b *testing.B, records, bytes int64, recordsUnit, bytesUnit string) {
	seconds := b.Elapsed().Seconds()
	if seconds == 0 {
		return
	}
	b.ReportMetric(float64(records)/seconds, recordsUnit)
	b.ReportMetric(float64(bytes)/(1<<20)/seconds, bytesUnit)
}
