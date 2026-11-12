package sqlite_test

import (
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PostgreSQL notices", func() {
	It("streams raw notices before a program ends and retains them with its result", func() {
		db := dbtest.ForGinkgo(dbtest.Options{Name: "query_notices"})
		var events []query.BatchEvent
		ctx := query.WithBatchObserver(context.New(), func(event query.BatchEvent) {
			events = append(events, event)
		})
		result, err := query.Execute(ctx, postgresProfile(db.DSN(), `DO $$ BEGIN
RAISE NOTICE '::progress::1/2';
END $$;
GO
SELECT 2 AS completed;`), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(events).To(HaveLen(3))
		Expect(events[0]).To(Equal(query.BatchEvent{Batch: 1, Run: 1, StartLine: 1, Message: "::progress::1/2"}))
		Expect(events[1].Done).To(BeTrue())
		Expect(result.Batches[0].Messages).To(Equal([]string{"::progress::1/2"}))
	})

	It("streams notices from a function called through a standalone SELECT", func() {
		db := dbtest.ForGinkgo(dbtest.Options{Name: "function_notices"})
		_, err := db.SQL().Exec(`CREATE FUNCTION report_progress() RETURNS integer LANGUAGE plpgsql AS $$
BEGIN RAISE NOTICE '::status::working'; RETURN 5; END $$`)
		Expect(err).NotTo(HaveOccurred())
		var messages []string
		ctx := query.WithBatchObserver(context.New(), func(event query.BatchEvent) {
			if !event.Done {
				messages = append(messages, event.Message)
			}
		})
		result, err := query.Execute(ctx, postgresProfile(db.DSN(), "SELECT report_progress() AS completed"), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(messages).To(Equal([]string{"::status::working"}))
		Expect(result.Rows).To(Equal([]query.Row{{"completed": int64(5)}}))
	})

	It("delivers notices before surfacing a database error", func() {
		db := dbtest.ForGinkgo(dbtest.Options{Name: "failed_notices"})
		var messages []string
		ctx := query.WithBatchObserver(context.New(), func(event query.BatchEvent) {
			if !event.Done {
				messages = append(messages, event.Message)
			}
		})
		_, err := query.Execute(ctx, postgresProfile(db.DSN(), `DO $$ BEGIN
RAISE NOTICE '::progress::1/2';
RAISE EXCEPTION 'batch failed';
END $$`), nil)
		Expect(err).To(MatchError(ContainSubstring("batch failed")))
		Expect(messages).To(Equal([]string{"::progress::1/2"}))
	})
})
