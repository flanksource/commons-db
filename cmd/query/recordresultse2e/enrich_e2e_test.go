// Specs for a result type that enriches its rows as they are read: pages,
// follows and exports all carry the enriched column, and an enricher that
// breaks the rows it was given fails the read.
package recordresultse2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"
)

// shardOf is the enrichment every spec expects: the row's database — a
// tally's name — sharded.
func shardOf(row query.Row) string {
	if db, ok := row["db"]; ok {
		return fmt.Sprint(db) + "-shard"
	}
	return fmt.Sprint(row["name"]) + "-shard"
}

func shardRows(_ context.Context, rows []query.Row) ([]query.Row, error) {
	for _, row := range rows {
		row["shard"] = shardOf(row)
	}
	return rows, nil
}

var shardColumns = []query.ColumnDef{{Name: "shard", Type: query.ColumnTypeString}}

var _ = Describe("a record result type that enriches its rows", func() {
	serve := func(enrich func(context.Context, []query.Row) ([]query.Row, error)) followServer {
		return newFollowServerWith(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: recordresultstest.LocalSettings(recordstore.BackendSQLite),
			Register: func(registry *recordresults.Registry) error {
				if err := recordresults.RegisterResultType(registry, recordresults.ResultType[recordresultstest.SampleEvent]{
					Kind: "sample_event", Title: "Sample events", TimeColumn: "at", Follow: true,
					Enrich: enrich, EnrichColumns: shardColumns,
				}); err != nil {
					return err
				}
				return recordresults.RegisterResultType(registry, recordresults.ResultType[sampleTally]{
					Kind: "sample_tally", Title: "Sample tallies", Enrich: enrich, EnrichColumns: shardColumns,
				})
			},
		})
	}

	It("serves the enriched column in every page, and offers no filter or sort on it", func() {
		server := serve(shardRows)
		server.appendEvents(1, 3)

		rows := server.pagedRows("stream=run-1")
		Expect(rows).To(HaveLen(3))
		for _, row := range rows {
			Expect(row).To(HaveKeyWithValue("shard", shardOf(row)))
		}
		response := server.do(http.MethodGet, followedProfile+"?stream=run-1&sort=shard", http.Header{"Accept": {"application/json"}})
		Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
	})

	It("serves the enriched column in an export of the whole stream", func() {
		server := serve(shardRows)
		server.appendEvents(1, 5)

		response := server.do(http.MethodGet, followedProfile+"?stream=run-1&scope=all&format=json", http.Header{"Accept": {"application/json"}})
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		var exported []map[string]any
		Expect(json.NewDecoder(response.Body).Decode(&exported)).To(Succeed())
		Expect(exported).To(HaveLen(5))
		for _, row := range exported {
			Expect(row).To(HaveKeyWithValue("shard", shardOf(row)))
		}
	})

	It("enriches the rows a follow streams", func() {
		server := serve(shardRows)
		server.appendEvents(1, 2)
		status, body := server.startFollow("follow=true&stream=run-1&afterSeq=2")
		Expect(status).To(Equal(http.StatusCreated), body)
		var info query.SessionInfo
		Expect(json.Unmarshal([]byte(body), &info)).To(Succeed())
		events, disconnect := server.subscribe(info.ID, "")
		DeferCleanup(disconnect)

		server.appendEvents(3, 4)
		rows, _, _ := followedFrom(events, 2)
		for _, row := range rows {
			Expect(row).To(HaveKeyWithValue("shard", shardOf(row)))
		}
	})

	It("enriches the pages of a type that does not follow", func() {
		server := serve(shardRows)
		_, err := recordstore.AppendTyped(context.Background(), server.results.Backend, "run-2", "sample_tally", []sampleTally{{Name: "oipa", Count: 1}})
		Expect(err).ToNot(HaveOccurred())

		response := server.do(http.MethodGet, unfollowedProfile+"?stream=run-2", http.Header{"Accept": {"application/json"}})
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		var rows []map[string]any
		Expect(json.NewDecoder(response.Body).Decode(&rows)).To(Succeed())
		Expect(rows).To(ConsistOf(HaveKeyWithValue("shard", "oipa-shard")))
	})

	DescribeTable("fails a read whose enricher breaks the rows it was given",
		func(enrich func(context.Context, []query.Row) ([]query.Row, error)) {
			server := serve(enrich)
			server.appendEvents(1, 3)
			response := server.do(http.MethodGet, followedProfile+"?stream=run-1", http.Header{"Accept": {"application/json"}})
			Expect(response.StatusCode).To(BeNumerically(">=", http.StatusBadRequest))
		},
		Entry("dropping a row", func(_ context.Context, rows []query.Row) ([]query.Row, error) { return rows[1:], nil }),
		Entry("changing a stored column", func(_ context.Context, rows []query.Row) ([]query.Row, error) {
			for _, row := range rows {
				row["db"] = "rewritten"
			}
			return rows, nil
		}),
		Entry("adding an undeclared column", func(_ context.Context, rows []query.Row) ([]query.Row, error) {
			for _, row := range rows {
				row["surprise"] = true
			}
			return rows, nil
		}),
		Entry("failing", func(context.Context, []query.Row) ([]query.Row, error) { return nil, errors.New("enricher down") }),
	)
})
