package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing/fstest"
	"time"

	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlclient"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/inspect/sql/inspector"
)

// SQL Server integration runs against COMMONS_DB_SQLSERVER_URL (an admin
// sqlserver:// URL with no database), or, with COMMONS_DB_SQLSERVER_TEST=1,
// against an ephemeral mcr.microsoft.com/mssql/server:2022-latest container
// started through the docker CLI. With neither set the specs are skipped; once
// opted in, failing to reach a server is a failure.
const (
	sqlServerImage    = "mcr.microsoft.com/mssql/server:2022-latest"
	sqlServerPassword = "Acme-Strong-Passw0rd"
)

const integrationTablesHCL = `schema "dbo" {}

table "acme_orders" {
  schema = schema.dbo
  column "id" {
    type = int
    identity {
      seed      = 1
      increment = 1
    }
  }
  column "status" {
    type    = nvarchar(20)
    default = "open"
  }
  column "total" {
    type    = decimal(18, 2)
    default = 0
  }
  column "created_at" {
    type    = datetime2(3)
    default = sql("sysutcdatetime()")
  }
  primary_key {
    columns = [column.id]
  }
  index "IX_acme_orders_status" {
    columns = [column.status]
    include = [column.total]
    where   = "status <> 'closed'"
  }
  check "CK_acme_orders_total" {
    expr = "total >= 0"
  }
}

table "acme_order_lines" {
  schema = schema.dbo
  column "line_id" {
    type = uniqueidentifier
  }
  column "order_id" {
    type = int
  }
  column "sku" {
    type = sql("varchar(max)")
    null = true
  }
  primary_key {
    columns  = [column.line_id]
    clustered = false
  }
  foreign_key "FK_acme_order_lines_order" {
    columns     = [column.order_id]
    ref_columns = [table.acme_orders.column.id]
    on_delete   = CASCADE
  }
}
`

const integrationCountOrders = `-- Counts a customer's orders.
CREATE PROCEDURE dbo.acme_count_orders
    @status NVARCHAR(20),
    @count INT OUTPUT
AS
BEGIN
    SET NOCOUNT ON;
    IF NOT EXISTS (SELECT 1 FROM dbo.acme_orders WHERE status = @status)
        RETURN;
    SET @count = (SELECT COUNT(*) FROM dbo.acme_orders WHERE status = @status);
END
GO
`

const integrationOrderTotal = `CREATE OR ALTER FUNCTION dbo.acme_order_total(@order_id INT)
RETURNS DECIMAL(18, 2)
AS
BEGIN
    RETURN (SELECT total FROM dbo.acme_orders WHERE id = @order_id);
END
`

func integrationBundle() fstest.MapFS {
	return fstest.MapFS{
		"db/schema.hcl":                     file(integrationTablesHCL),
		"db/routines/acme_count_orders.sql": file(integrationCountOrders),
		"db/routines/acme_order_total.sql":  file(integrationOrderTotal),
	}
}

var integrationOptions = []Option{WithDir("db"), WithRoutines("db/routines"), WithName("acme/integration")}

var _ = Describe("SQL Server migrations", Ordered, Label("integration"), func() {
	var adminURL string

	BeforeAll(func(ctx SpecContext) {
		adminURL = sqlServerAdminURL(ctx)
	})

	DescribeTable("reconcile tables and routines idempotently",
		func(ctx SpecContext, compatibilityLevel int, via connectionForm) {
			dsn, db := sqlServerDatabase(ctx, adminURL, compatibilityLevel)
			reconcileScenario(ctx, sqlServerApplier(dsn, db, via), dsn, db)
		},
		Entry("at the engine's default compatibility level", 0, viaURL),
		Entry("at compatibility level 100", 100, viaURL),
		Entry("through an ADO connection string", 0, viaADO),
		Entry("through WithDB on the caller's handle, which stays open", 0, viaHandle),
	)

	It("reads the migrated database through the catalog inspector", func(ctx SpecContext) {
		dsn, db := sqlServerDatabase(ctx, adminURL, 0)
		Expect(Apply(ctx, dsn, integrationBundle(), integrationOptions...)).To(Succeed())
		inspectCatalog(ctx, db)
	})
})

// connectionForm is how a spec hands the database to Apply.
type connectionForm string

const (
	viaURL    connectionForm = "sqlserver:// URL"
	viaADO    connectionForm = "ADO connection string"
	viaHandle connectionForm = "WithDB handle"
)

// sqlServerApplier applies a bundle to the database at dsn, whose open
// handle is db, in the given connection form.
func sqlServerApplier(dsn string, db *sql.DB, via connectionForm) func(context.Context, fstest.MapFS) error {
	return func(ctx context.Context, bundle fstest.MapFS) error {
		switch via {
		case viaADO:
			return Apply(ctx, adoConnectionString(dsn), bundle, integrationOptions...)
		case viaHandle:
			return Apply(ctx, "", bundle, append([]Option{WithDB(db)}, integrationOptions...)...)
		default:
			return Apply(ctx, dsn, bundle, integrationOptions...)
		}
	}
}

// adoConnectionString rewrites a sqlserver:// URL as the equivalent ADO
// key=value connection string.
func adoConnectionString(dsn string) string {
	u, err := url.Parse(dsn)
	Expect(err).ToNot(HaveOccurred())
	password, _ := u.User.Password()
	pairs := []string{"server=" + u.Hostname(), "user id=" + u.User.Username(), "password=" + password,
		"database=" + u.Query().Get("database")}
	if port := u.Port(); port != "" {
		pairs = append(pairs, "port="+port)
	}
	return strings.Join(pairs, ";")
}

func reconcileScenario(ctx context.Context, apply func(context.Context, fstest.MapFS) error, dsn string, db *sql.DB) {
	bundle := integrationBundle()
	By("applying a table, an index, a foreign key and two routines")
	Expect(apply(ctx, bundle)).To(Succeed())
	Expect(pendingSQLServerChanges(ctx, dsn, bundle)).To(BeEmpty())
	_, err := db.ExecContext(ctx, "CREATE INDEX IX_dba_tuning ON dbo.acme_orders (created_at)")
	Expect(err).ToNot(HaveOccurred())
	before := modifyDates(ctx, db)
	Expect(before).To(HaveLen(4))

	By("re-applying the same bundle, which changes nothing")
	Expect(pendingSQLServerChanges(ctx, dsn, bundle)).To(BeEmpty())
	Expect(apply(ctx, bundle)).To(Succeed())
	Expect(modifyDates(ctx, db)).To(Equal(before))
	Expect(objectExists(ctx, db, "IX_dba_tuning")).To(BeTrue(), "an index the bundle does not declare is kept")

	By("editing one routine's body, which alters exactly that routine")
	bundle["db/routines/acme_order_total.sql"] = file(strings.Replace(integrationOrderTotal, "RETURN (SELECT total", "RETURN (SELECT total * 2", 1))
	Expect(changeKinds(pendingSQLServerChanges(ctx, dsn, bundle))).To(Equal([]string{"*schema.ModifyFunc"}))
	Expect(apply(ctx, bundle)).To(Succeed())
	after := modifyDates(ctx, db)
	Expect(after["acme_order_total"]).To(BeTemporally(">", before["acme_order_total"]))
	delete(after, "acme_order_total")
	delete(before, "acme_order_total")
	Expect(after).To(Equal(before))
	Expect(pendingSQLServerChanges(ctx, dsn, bundle)).To(BeEmpty())

	By("removing a routine from the bundle, which leaves it in place")
	delete(bundle, "db/routines/acme_count_orders.sql")
	Expect(apply(ctx, bundle)).To(Succeed())
	Expect(objectExists(ctx, db, "acme_count_orders")).To(BeTrue())
}

func pendingSQLServerChanges(ctx context.Context, dsn string, bundle fstest.MapFS) []schema.Change {
	desired, err := sqlServerDesired(bundle, resolveOptions(integrationOptions))
	Expect(err).ToNot(HaveOccurred())
	client, err := sqlclient.Open(ctx, dsn)
	Expect(err).ToNot(HaveOccurred())
	defer client.Close() //nolint:errcheck
	changes, err := sqlServerChanges(ctx, client, desired, nil)
	Expect(err).ToNot(HaveOccurred())
	return changes
}

func changeKinds(changes []schema.Change) []string {
	kinds := make([]string, len(changes))
	for i, change := range changes {
		kinds[i] = fmt.Sprintf("%T", change)
	}
	return kinds
}

func modifyDates(ctx context.Context, db *sql.DB) map[string]time.Time {
	rows, err := db.QueryContext(ctx, `SELECT name, modify_date FROM sys.objects
		WHERE name IN ('acme_orders', 'acme_order_lines', 'acme_count_orders', 'acme_order_total')`)
	Expect(err).ToNot(HaveOccurred())
	defer rows.Close() //nolint:errcheck
	dates := map[string]time.Time{}
	for rows.Next() {
		var name string
		var modified time.Time
		Expect(rows.Scan(&name, &modified)).To(Succeed())
		dates[name] = modified
	}
	Expect(rows.Err()).ToNot(HaveOccurred())
	return dates
}

func objectExists(ctx context.Context, db *sql.DB, name string) bool {
	var count int
	Expect(db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM sys.objects WHERE name = @p1)
		+ (SELECT COUNT(*) FROM sys.indexes WHERE name = @p1)`, name).Scan(&count)).To(Succeed())
	return count > 0
}

func inspectCatalog(ctx context.Context, db *sql.DB) {
	bounds := &inspector.Bounds{Rows: 1000, DefinitionBytes: 1 << 20}
	catalog, err := inspector.NewInspector(db, "sqlserver", bounds)
	Expect(err).ToNot(HaveOccurred())
	tables, err := catalog.GetTables(ctx, "dbo", "table")
	Expect(err).ToNot(HaveOccurred())
	Expect(tableNames(tables)).To(ConsistOf("acme_orders", "acme_order_lines"))
	indexes, err := catalog.GetIndexes(ctx, "dbo")
	Expect(err).ToNot(HaveOccurred())
	Expect(indexes).To(ContainElement(HaveField("IndexName", "IX_acme_orders_status")))
	fks, err := catalog.GetForeignKeys(ctx, "dbo")
	Expect(err).ToNot(HaveOccurred())
	Expect(fks).To(ContainElement(HaveField("ConstraintName", "FK_acme_order_lines_order")))
	procs, err := catalog.GetStoredProcs(ctx, "dbo")
	Expect(err).ToNot(HaveOccurred())
	Expect(procs).To(ContainElements(HaveField("Name", "acme_count_orders"), HaveField("Name", "acme_order_total")))
	params, err := catalog.GetProcParams(ctx, "dbo")
	Expect(err).ToNot(HaveOccurred())
	Expect(params).ToNot(BeEmpty())
	Expect(bounds.Truncated).To(BeFalse())
}

func tableNames(tables []*inspector.Table) []string {
	names := make([]string, len(tables))
	for i, t := range tables {
		names[i] = t.Name
	}
	return names
}

// sqlServerDatabase creates a database for one spec, at the given
// compatibility level when it is not zero, and drops it afterwards.
func sqlServerDatabase(ctx context.Context, adminURL string, compatibilityLevel int) (string, *sql.DB) {
	admin, err := sql.Open("sqlserver", adminURL)
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(admin.Close)
	name := fmt.Sprintf("commons_db_test_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(ctx, "CREATE DATABASE ["+name+"]")
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(func(ctx SpecContext) {
		_, err := admin.ExecContext(ctx, "ALTER DATABASE ["+name+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+name+"]")
		Expect(err).ToNot(HaveOccurred())
	})
	if compatibilityLevel != 0 {
		_, err = admin.ExecContext(ctx, fmt.Sprintf("ALTER DATABASE [%s] SET COMPATIBILITY_LEVEL = %d", name, compatibilityLevel))
		Expect(err).ToNot(HaveOccurred())
	}
	u, err := url.Parse(adminURL)
	Expect(err).ToNot(HaveOccurred())
	query := u.Query()
	query.Set("database", name)
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlserver", u.String())
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(db.Close)
	return u.String(), db
}

// sqlServerAdminURL returns the configured server, or starts a container.
func sqlServerAdminURL(ctx context.Context) string {
	if configured := strings.TrimSpace(os.Getenv("COMMONS_DB_SQLSERVER_URL")); configured != "" {
		waitForSQLServer(ctx, configured)
		return configured
	}
	if os.Getenv("COMMONS_DB_SQLSERVER_TEST") == "" {
		Skip("set COMMONS_DB_SQLSERVER_URL, or COMMONS_DB_SQLSERVER_TEST=1 to start " + sqlServerImage)
	}
	id := docker(ctx, "run", "-d", "--rm", "-e", "ACCEPT_EULA=Y", "-e", "MSSQL_SA_PASSWORD="+sqlServerPassword,
		"-p", "127.0.0.1::1433", sqlServerImage)
	DeferCleanup(func(ctx SpecContext) { docker(ctx, "rm", "-f", id) })
	address := strings.TrimSpace(docker(ctx, "port", id, "1433/tcp"))
	adminURL := (&url.URL{Scheme: "sqlserver", User: url.UserPassword("sa", sqlServerPassword), Host: address}).String()
	waitForSQLServer(ctx, adminURL)
	return adminURL
}

func docker(ctx context.Context, args ...string) string {
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	Expect(err).ToNot(HaveOccurred(), "docker %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

func waitForSQLServer(ctx context.Context, adminURL string) {
	db, err := sql.Open("sqlserver", adminURL)
	Expect(err).ToNot(HaveOccurred())
	defer db.Close() //nolint:errcheck
	Eventually(func(ctx context.Context) error { return db.PingContext(ctx) }).
		WithContext(ctx).WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())
}
