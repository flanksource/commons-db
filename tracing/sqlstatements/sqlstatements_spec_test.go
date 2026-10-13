// Specs for the sql trace kind: statements run through the query providers and
// gorm are stored per connection, filtered by duration, with secrets masked.

package sqlstatements_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"

	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/sqlstatements"
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

var _ = Describe("sql trace kind", func() {
	var env tracingtest.Env
	var database *sql.DB

	BeforeEach(func() {
		env = tracingtest.NewEnv(map[string]tracing.TracePlugin{sqlstatements.RowKind: sqlstatements.Kind()})
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

	captureSession := func(params string, statements func()) query.SessionInfo {
		session := env.Start(sqlstatements.RowKind, params)
		tracingtest.Running(session)
		statements()
		session.Stop("spec")
		info := tracingtest.Ended(session)
		Expect(info.Error).To(BeEmpty())
		return info
	}

	capture := func(params string, statements func()) []recordstore.Row {
		return env.Rows(captureSession(params, statements))
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

	Context("with the server's own database", func() {
		var gormDB *gorm.DB

		BeforeEach(func() {
			var err error
			gormDB, err = db.NewGorm(filepath.Join(GinkgoT().TempDir(), "own.db"), db.DefaultGormConfig())
			Expect(err).ToNot(HaveOccurred())
			own, err := gormDB.DB()
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(own.Close)
		})

		connectionsOf := func(params string) []any {
			rows := capture(params, func() {
				read("events-db", "SELECT id FROM events")
				read("other-db", "SELECT id FROM events")
				read("", "SELECT id FROM events")
				Expect(gormDB.Exec("CREATE TABLE notes (id INTEGER PRIMARY KEY)").Error).To(Succeed())
			})
			connections := []any{}
			for _, row := range rows {
				connections = append(connections, row["connection"])
			}
			return connections
		}

		It("stores every other connection's statements, named or not, but not its own database's, for *", func() {
			Expect(connectionsOf(`{"connections": ["*"]}`)).To(ConsistOf("events-db", "other-db", ""))
		})

		It("stores only its own database's statements for self", func() {
			Expect(connectionsOf(`{"connections": ["self"]}`)).To(ConsistOf("self"))
		})

		It("stores both when it names * and self", func() {
			Expect(connectionsOf(`{"connections": ["*", "self"]}`)).To(ConsistOf("events-db", "other-db", "", "self"))
		})
	})

	It("refuses a capture that names no connections", func() {
		for _, params := range []string{`{}`, `{"connections": []}`, `{"connections": [""]}`, `{"connections": ["events-db", " "]}`} {
			Expect(sqlstatements.Kind().ValidateParams(json.RawMessage(params))).
				To(MatchError(ContainSubstring("connections")), params)
		}
	})

	It("stores only the statements that ran at least as long as its minimum duration", func() {
		Expect(capture(`{"connections": ["events-db"], "minDuration": "1h"}`, func() { read("events-db", "SELECT id FROM events") })).To(BeEmpty())
	})

	It("masks secrets written into a statement", func() {
		rows := capture(`{"connections": ["*"]}`, func() { read("events-db", "SELECT id FROM events WHERE message <> 'password=hunter2'") })
		Expect(rows).To(HaveLen(1))
		stored, err := json.Marshal(rows[0])
		Expect(err).ToNot(HaveOccurred())
		Expect(string(stored)).ToNot(ContainSubstring("hunter2"))
	})

	It("refuses a minimum duration that is not one", func() {
		Expect(sqlstatements.Kind().ValidateParams(json.RawMessage(`{"connections": ["*"], "minDuration": "soon"}`))).
			To(MatchError(ContainSubstring("minDuration")))
	})

	Context("read back through its result profile", func() {
		const (
			first   = "SELECT id FROM events WHERE id = 1"
			second  = "SELECT id FROM events WHERE id = 2"
			notes   = "SELECT id FROM notes"
			missing = "SELECT id FROM missing"
		)
		var info query.SessionInfo

		BeforeEach(func() {
			_, err := database.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY); INSERT INTO notes (id) VALUES (1);`)
			Expect(err).ToNot(HaveOccurred())
			info = captureSession(`{"connections": ["events-db"]}`, func() {
				read("events-db", first)
				read("events-db", second)
				read("events-db", notes)
				_, err := providers.ReadSQLPage(context.Background(), database, models.ConnectionTypeSQLite, providers.SQLPageRequest{
					Connection: "events-db", Query: missing, Page: query.PageRequest{Limit: 10},
				})
				Expect(err).To(MatchError(ContainSubstring("no such table")))
			})
		})

		statementsOf := func(profile string, params map[string]any) []string {
			var statements []string
			for _, row := range env.Read(profile, info.Events.Stream, query.PageRequest{Limit: 50}, params) {
				Expect(row["sql"]).To(BeAssignableToTypeOf(""))
				for _, statement := range []string{first, second, notes, missing} {
					if strings.Contains(row["sql"].(string), statement) {
						statements = append(statements, statement)
					}
				}
			}
			return statements
		}

		It("filters by the tables a statement names, its op and connection, and searches its SQL and error", func() {
			Expect(statementsOf(sqlstatements.RowKind, nil)).To(ConsistOf(first, second, notes, missing))
			Expect(statementsOf(sqlstatements.RowKind, map[string]any{"filter.tables": "notes"})).To(ConsistOf(notes))
			Expect(statementsOf(sqlstatements.RowKind, map[string]any{"filter.tables": "!events"})).To(ConsistOf(notes, missing))
			Expect(statementsOf(sqlstatements.RowKind, map[string]any{"filter.op": "!SELECT"})).To(BeEmpty())
			Expect(statementsOf(sqlstatements.RowKind, map[string]any{"filter.connection": "events-db"})).To(HaveLen(4))
			Expect(statementsOf(sqlstatements.RowKind, map[string]any{"q": "NO SUCH TABLE"})).To(ConsistOf(missing))
		})

		It("serves the statements that ran more than once as one row each, with how often", func() {
			rows := env.Read(sqlstatements.RowKind+"/repeats", info.Events.Stream, query.PageRequest{Limit: 50}, nil)
			Expect(rows).To(HaveLen(1))
			Expect(rows[0]).To(HaveKeyWithValue("sql", ContainSubstring("FROM events WHERE id = ")))
			Expect(rows[0]).To(And(
				HaveKeyWithValue("repeatKey", sqlstatements.RepeatKey(rows[0]["sql"].(string))),
				HaveKeyWithValue("runs", BeNumerically("==", 2)),
			))
		})

		It("reports the summary of the statements it stored as the session ends", func() {
			report := tracingtest.Summary(info).Report
			Expect(report).To(HaveKeyWithValue("statements", float64(4)))
			Expect(report).To(HaveKeyWithValue("errors", float64(1)))
			Expect(report).To(HaveKeyWithValue("repeated", float64(2)))
			Expect(report).To(HaveKeyWithValue("tables", ContainElements("events", "notes", "missing")))
			Expect(report).To(HaveKeyWithValue("byOp", HaveKeyWithValue("SELECT", float64(4))))
		})
	})
})
