// Live specs for the sql_xevent trace kind against a real SQL Server: a
// statement run during a capture is stored, and the XE session is dropped.

package xevent_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers"

	"github.com/flanksource/commons-db/tracing/traces"
	"github.com/flanksource/commons-db/tracing/traces/tracestest"
	"github.com/flanksource/commons-db/tracing/traces/xevent"
)

// Set COMMONS_DB_SQLSERVER_URL to run these, as a login allowed to create
// event sessions: sqlserver://sa:secret@localhost:1433?database=master
var _ = Describe("sql_xevent trace kind (live)", func() {
	It("stores a statement run during the capture and drops its XE session", func() {
		address := strings.TrimSpace(os.Getenv("COMMONS_DB_SQLSERVER_URL"))
		if address == "" {
			Skip("COMMONS_DB_SQLSERVER_URL is not set")
		}
		ctx := dbcontext.New().WithConnectionResolver(func(dbcontext.Context, string) (*models.Connection, error) {
			return &models.Connection{Name: "live", Type: models.ConnectionTypeSQLServer, URL: address}, nil
		})
		client, release, err := providers.OpenSQLServer(ctx, "connection://live")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(release)

		env := tracestest.NewEnv(map[string]traces.TracePlugin{"sql_xevent": xevent.Kind()})
		unique := time.Now().UnixNano()
		name := fmt.Sprintf("commons-db-live-%d", unique)
		managed, err := env.Runtime.Start(ctx, traces.StartRequest{Kind: "sql_xevent", Params: json.RawMessage(
			fmt.Sprintf(`{"connection": "connection://live", "sessionName": %q, "poll": "200ms"}`, name))})
		Expect(err).ToNot(HaveOccurred())
		session := managed.Session()
		tracestest.Running(session)

		running := func() int {
			var count int
			Expect(client.QueryRow(`SELECT COUNT(*) FROM sys.server_event_sessions WHERE name LIKE @p1`, name+"%").
				Scan(&count)).To(Succeed())
			return count
		}
		Expect(running()).To(Equal(1))

		marker := fmt.Sprintf("commons_db_live_%d", unique)
		var echoed string
		Expect(client.QueryRow("SELECT '" + marker + "'").Scan(&echoed)).To(Succeed())
		Expect(echoed).To(Equal(marker))
		time.Sleep(time.Second)

		session.Stop("spec")
		info := tracestest.Ended(session)
		Expect(info.State).To(Equal(query.SessionStopped))
		Expect(info.Error).To(BeEmpty())
		Expect(env.Sealed(info)).To(BeTrue())

		var statements []string
		for _, row := range env.Rows(info) {
			if sql, _ := row["sql"].(string); strings.Contains(sql, marker) {
				statements = append(statements, sql)
			}
		}
		Expect(statements).ToNot(BeEmpty())
		Expect(running()).To(Equal(0))
	})
})
