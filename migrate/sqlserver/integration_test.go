//go:build integration

package sqlserver

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/commons-db/inspect/sql/snapshot"
	"github.com/flanksource/sqlglot-go/migrate"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
	mssql "github.com/microsoft/go-mssqldb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const integrationFixture = `CREATE TABLE dbo.orders (
	id int IDENTITY(1000,5) NOT NULL CONSTRAINT PK_orders PRIMARY KEY,
	label nvarchar(20) NULL CONSTRAINT DF_orders_label DEFAULT (N'pending'),
	total decimal(18,2) NOT NULL CONSTRAINT DF_orders_total DEFAULT ((0)),
	CONSTRAINT CK_orders_total CHECK (total >= 0)
);
CREATE INDEX IX_orders_total ON dbo.orders(total DESC) INCLUDE(label) WHERE total > 0;
INSERT INTO dbo.orders(label,total) VALUES (N'cannot-convert',10);`

var _ = Describe("Isolated SQL Server artifacts", func() {
	var db *sql.DB
	var baseline migrate.Schema

	BeforeEach(func(ctx SpecContext) {
		dsn := os.Getenv("COMMONS_DB_SQLSERVER_ARTIFACT_TEST_URL")
		Expect(dsn).NotTo(BeEmpty(), "provide an explicit admin URL for an isolated SQL Server test instance")
		parsed, err := url.Parse(dsn)
		Expect(err).NotTo(HaveOccurred())
		Expect(parsed.Scheme).To(Equal("sqlserver"))
		Expect(parsed.Query().Get("database")).To(BeElementOf("", "master"))
		admin, err := sql.Open("sqlserver", dsn)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(admin.Close)
		name := fmt.Sprintf("commons_db_artifact_test_%d", time.Now().UnixNano())
		_, err = admin.ExecContext(ctx, "CREATE DATABASE ["+name+"]")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) {
			_, err := admin.ExecContext(ctx, "ALTER DATABASE ["+name+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+name+"]")
			Expect(err).NotTo(HaveOccurred())
		})
		_, err = admin.ExecContext(ctx, "ALTER DATABASE ["+name+"] SET COMPATIBILITY_LEVEL = 100")
		Expect(err).NotTo(HaveOccurred())
		query := parsed.Query()
		query.Set("database", name)
		parsed.RawQuery = query.Encode()
		db, err = sql.Open("sqlserver", parsed.String())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		_, err = db.ExecContext(ctx, integrationFixture)
		Expect(err).NotTo(HaveOccurred())
		for _, ddl := range []string{
			"CREATE VIEW dbo.order_ids AS SELECT id FROM dbo.orders",
			"CREATE FUNCTION dbo.order_scale(@amount decimal(18,2)) RETURNS decimal(18,2) AS BEGIN RETURN @amount; END",
			"CREATE PROCEDURE dbo.order_count AS SELECT COUNT(*) FROM dbo.orders",
			"CREATE TRIGGER dbo.orders_audit ON dbo.orders AFTER INSERT AS BEGIN SET NOCOUNT ON; END",
			"DISABLE TRIGGER dbo.orders_audit ON dbo.orders",
		} {
			_, err = db.ExecContext(ctx, ddl)
			Expect(err).NotTo(HaveOccurred())
		}
		inspected, err := snapshot.Inspect(ctx, db, snapshot.Options{Schemas: []string{"dbo"}})
		Expect(err).NotTo(HaveOccurred())
		baseline = inspected.Schema
	})

	compile := func(desired migrate.Schema) Request {
		artifact, err := planner.BuildArtifact(migrate.ArtifactRequest{Schemas: []string{"dbo"}, Request: migrate.Request{Current: baseline, Desired: desired}})
		Expect(err).NotTo(HaveOccurred())
		request := Request{Artifact: artifact, Hash: artifact.Hash}
		for _, change := range artifact.Plan.Changes {
			if change.Destructive {
				request.Acknowledgements = append(request.Acknowledgements, change.ID)
			}
		}
		return request
	}

	copyBaseline := func() migrate.Schema {
		body, err := json.Marshal(baseline)
		Expect(err).NotTo(HaveOccurred())
		var desired migrate.Schema
		Expect(json.Unmarshal(body, &desired)).To(Succeed())
		return desired
	}

	It("changes a column, function and trigger state and then becomes a no-op", func(ctx SpecContext) {
		desired := copyBaseline()
		desired.Tables[0].Columns[1].Type = "nvarchar(100)"
		desired.Tables[0].Indexes = append(desired.Tables[0].Indexes, migrate.Index{Name: "IX_orders_label", Columns: []migrate.IndexColumn{{Name: "label"}}, Filter: "[label] IS NOT NULL"})
		for i := range desired.Definitions {
			d := &desired.Definitions[i]
			if d.Kind == "FUNCTION" {
				d.SQL = strings.Replace(d.SQL, "RETURN @amount;", "RETURN @amount * 2;", 1)
			}
			if d.Kind == "TRIGGER" {
				d.Disabled = false
			}
		}
		request := compile(desired)
		result, err := Apply(ctx, db, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(Applied))
		var value int
		Expect(db.QueryRowContext(ctx, "SELECT CAST(dbo.order_scale(5) AS int)").Scan(&value)).To(Succeed())
		Expect(value).To(Equal(10))
		result, err = Apply(ctx, db, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{Hash: request.Hash, Outcome: AlreadyApplied}))
	})

	It("restores dropped defaults and indexes when a later conversion fails", func(ctx SpecContext) {
		desired := copyBaseline()
		desired.Tables[0].Columns[1].Type = "int"
		desired.Tables[0].Columns[1].Collation = ""
		desired.Tables[0].Columns[1].Default = nil
		desired.Tables[0].Indexes = slices.DeleteFunc(desired.Tables[0].Indexes, func(index migrate.Index) bool { return !index.Primary })
		request := compile(desired)
		result, err := Apply(ctx, db, request)
		var conversion mssql.Error
		Expect(errors.As(err, &conversion)).To(BeTrue())
		Expect(conversion.Number).To(Equal(int32(245)))
		Expect(result.Outcome).To(Equal(Failed))
		Expect(result.Steps).To(BeNumerically(">", 0))
		var exists int
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.default_constraints WHERE name='DF_orders_label'").Scan(&exists)).To(Succeed())
		Expect(exists).To(Equal(1))
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.indexes WHERE name='IX_orders_total'").Scan(&exists)).To(Succeed())
		Expect(exists).To(Equal(1))
		actual, err := snapshot.Inspect(ctx, db, snapshot.Options{Schemas: []string{"dbo"}})
		Expect(err).NotTo(HaveOccurred())
		state, err := planner.CheckArtifact(request.Artifact, actual.Schema)
		Expect(err).NotTo(HaveOccurred())
		Expect(state).To(Equal(migrate.ArtifactPending))
	})

	It("refuses target drift before applying the artifact", func(ctx SpecContext) {
		desired := copyBaseline()
		desired.Tables[0].Columns[1].Type = "nvarchar(100)"
		request := compile(desired)
		_, err := db.ExecContext(ctx, "ALTER TABLE dbo.orders ADD extra bit NULL")
		Expect(err).NotTo(HaveOccurred())
		result, err := Apply(ctx, db, request)
		Expect(err).To(MatchError(ContainSubstring("baseline drift")))
		Expect(result).To(Equal(Result{Hash: request.Hash, Outcome: Failed}))
	})

	It("serializes different artifacts with a transaction-owned database lock", func(ctx SpecContext) {
		tx, err := db.BeginTx(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(tx.Rollback()).To(Succeed()) }()
		Expect(lock(ctx, tx, 0)).To(Succeed())
		desired := copyBaseline()
		desired.Tables[0].Columns[1].Type = "nvarchar(100)"
		request := compile(desired)
		request.LockTimeout = 10 * time.Millisecond
		result, err := Apply(ctx, db, request)
		Expect(err).To(MatchError(ContainSubstring("sp_getapplock returned -1")))
		Expect(result.Outcome).To(Equal(Failed))
	})
})
