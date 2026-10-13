// Specs for the sql_xevent result profile's tables filter: an event row is kept
// when one of the tables it touched is named, or none it touched is excluded.

package xevent_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/tracing/sqltrace"

	"github.com/flanksource/commons-db/tracing/traces"
	"github.com/flanksource/commons-db/tracing/traces/tracestest"
	"github.com/flanksource/commons-db/tracing/traces/xevent"
)

var _ = Describe("the sql_xevent result profile", func() {
	const (
		stream     = "capture-1"
		policies   = "SELECT * FROM AsPolicy"
		activities = "UPDATE AsActivity SET StatusCode = '01'"
		joined     = "SELECT * FROM AsPolicy p JOIN AsClient c ON c.ClientGUID = p.ClientGUID"
	)
	var env tracestest.Env

	BeforeEach(func() {
		env = tracestest.NewEnv(map[string]traces.TracePlugin{sqltrace.RowKind: xevent.Kind()})
		var rows []recordstore.Row
		for sql, tables := range map[string][]string{
			policies:   {"AsPolicy"},
			activities: {"AsActivity"},
			joined:     {"AsPolicy", "AsClient"},
		} {
			event := statement(51, sql, 0)
			event.Tables = tables
			row, err := recordstore.EncodeRow(sqltrace.FromEvent(event))
			Expect(err).ToNot(HaveOccurred())
			rows = append(rows, row)
		}
		_, err := env.Results.Backend.Append(context.Background(), stream, sqltrace.RowKind, rows)
		Expect(err).ToNot(HaveOccurred())
	})

	readSQL := func(params map[string]any) []string {
		var sql []string
		for _, row := range env.Read(sqltrace.RowKind, stream, query.PageRequest{Limit: 50}, params) {
			sql = append(sql, fmt.Sprint(row["sql"]))
		}
		return sql
	}

	It("keeps only the events that touched a table filter.tables names, or none it excludes", func() {
		Expect(readSQL(nil)).To(ConsistOf(policies, activities, joined))
		Expect(readSQL(map[string]any{"filter.tables": "AsClient"})).To(ConsistOf(joined))
		Expect(readSQL(map[string]any{"filter.tables": "AsActivity,AsClient"})).To(ConsistOf(activities, joined))
		Expect(readSQL(map[string]any{"filter.tables": "!AsPolicy"})).To(ConsistOf(activities))
	})
})
