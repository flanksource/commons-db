// Specs for the sql trace kind: statements run through the query providers and
// gorm are stored per connection, filtered by duration, with secrets masked.

package sqlstatements_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers"
	"github.com/flanksource/commons-db/recordstore"

	"github.com/flanksource/commons-db/tracing/traces"
	"github.com/flanksource/commons-db/tracing/traces/sqlstatements"
	"github.com/flanksource/commons-db/tracing/traces/tracestest"
)

var _ = Describe("sql trace kind", func() {
	var env tracestest.Env
	var database *sql.DB

	BeforeEach(func() {
		env = tracestest.NewEnv(map[string]traces.TracePlugin{"sql": sqlstatements.Kind()})
		var err error
		database, err = sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "events.db"))
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(database.Close)
		_, err = database.Exec(`CREATE TABLE events (id INTEGER PRIMARY KEY, message TEXT);
			INSERT INTO events (id, message) VALUES (1, 'one'), (2, 'two');`)
		Expect(err).ToNot(HaveOccurred())
	})

	read := func(connection, statement string) {
		_, err := providers.ReadSQLPage(context.Background(), database, models.ConnectionTypeSQLite, providers.SQLPageRequest{
			Connection: connection, Query: statement, Page: query.PageRequest{Limit: 10},
		})
		Expect(err).ToNot(HaveOccurred())
	}

	capture := func(params string, statements func()) []recordstore.Row {
		session := env.Start("sql", params)
		tracestest.Running(session)
		statements()
		session.Stop("spec")
		info := tracestest.Ended(session)
		Expect(info.Error).To(BeEmpty())
		return env.Rows(info)
	}

	It("stores the statements run on the connections it observes", func() {
		rows := capture(`{"connections": ["events-db"]}`, func() {
			read("events-db", "SELECT id, message FROM events ORDER BY id")
			read("other-db", "SELECT id FROM events")
		})
		Expect(rows).To(HaveLen(1))
		Expect(rows[0]["connection"]).To(Equal("events-db"))
		Expect(rows[0]["driver"]).To(Equal("sqlite"))
		Expect(rows[0]["sql"]).To(ContainSubstring("FROM events ORDER BY id"))
		Expect(rows[0]["rows"]).To(BeEquivalentTo(2))
		Expect(rows[0]["startedAt"]).ToNot(BeNil())
	})

	It("stores every connection's statements, and the context database's, when it names none", func() {
		gormDB, err := db.NewGorm(filepath.Join(GinkgoT().TempDir(), "own.db"), db.DefaultGormConfig())
		Expect(err).ToNot(HaveOccurred())
		own, err := gormDB.DB()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(own.Close)

		rows := capture(`{}`, func() {
			read("events-db", "SELECT id FROM events")
			Expect(gormDB.Exec("CREATE TABLE notes (id INTEGER PRIMARY KEY)").Error).To(Succeed())
		})
		connections := []any{}
		for _, row := range rows {
			connections = append(connections, row["connection"])
		}
		Expect(connections).To(ConsistOf("events-db", ""))
	})

	It("stores only the statements that ran at least as long as its minimum duration", func() {
		Expect(capture(`{"minDuration": "1h"}`, func() { read("events-db", "SELECT id FROM events") })).To(BeEmpty())
	})

	It("masks secrets written into a statement", func() {
		rows := capture(`{}`, func() { read("events-db", "SELECT id FROM events WHERE message <> 'password=hunter2'") })
		Expect(rows).To(HaveLen(1))
		stored, err := json.Marshal(rows[0])
		Expect(err).ToNot(HaveOccurred())
		Expect(string(stored)).ToNot(ContainSubstring("hunter2"))
	})

	It("refuses a minimum duration that is not one", func() {
		Expect(sqlstatements.Kind().ValidateParams(json.RawMessage(`{"minDuration": "soon"}`))).
			To(MatchError(ContainSubstring("minDuration")))
	})
})
