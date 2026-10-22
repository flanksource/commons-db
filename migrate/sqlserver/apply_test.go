package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/flanksource/commons-db/inspect/sql/snapshot"
	"github.com/flanksource/sqlglot-go/migrate"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestArtifactExecution(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL Server Artifact Execution")
}

func testRequest() Request {
	table := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Columns: []migrate.Column{{Name: "id", Type: "int"}}}
	artifact, err := planner.BuildArtifact(migrate.ArtifactRequest{Schemas: []string{"dbo"}, Request: migrate.Request{Desired: migrate.Schema{Tables: []migrate.Table{table}}}})
	Expect(err).NotTo(HaveOccurred())
	return Request{Artifact: artifact, Hash: artifact.Hash}
}

var _ = Describe("Reviewed artifact execution", func() {
	var db *sql.DB
	var mock sqlmock.Sqlmock
	var request Request
	var inspections int
	var inspect inspectFunc

	BeforeEach(func() {
		var err error
		db, mock, err = sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		request, inspections = testRequest(), 0
		inspect = func(_ context.Context, tx *sql.Tx, options snapshot.Options) (snapshot.Result, error) {
			Expect(tx).NotTo(BeNil())
			Expect(options.Schemas).To(Equal(request.Artifact.Schemas))
			inspections++
			if inspections == 1 {
				return snapshot.Result{Schema: request.Artifact.Request.Current}, nil
			}
			return snapshot.Result{Schema: request.Artifact.Request.Desired}, nil
		}
	})

	AfterEach(func() {
		Expect(mock.ExpectationsWereMet()).To(Succeed())
		mock.ExpectClose()
		Expect(db.Close()).To(Succeed())
	})

	begin := func(status int) {
		mock.ExpectBegin()
		mock.ExpectQuery("sp_getapplock").WithArgs(int64(0)).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(status))
	}

	It("executes the reviewed SQL in its own module batch, checks the postcondition and commits", func() {
		begin(0)
		mock.ExpectExec("sp_executesql").WithArgs(request.Artifact.Plan.Steps[0].SQL).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()
		result, err := apply(GinkgoT().Context(), db, request, inspect)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{Hash: request.Hash, Outcome: Applied, Steps: 1}))
		Expect(inspections).To(Equal(2))
	})

	It("returns already-applied without executing or committing DDL", func() {
		begin(1)
		mock.ExpectRollback()
		result, err := apply(GinkgoT().Context(), db, request, func(context.Context, *sql.Tx, snapshot.Options) (snapshot.Result, error) {
			return snapshot.Result{Schema: request.Artifact.Request.Desired}, nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{Hash: request.Hash, Outcome: AlreadyApplied}))
	})

	It("rolls back when the baseline has drifted", func() {
		begin(0)
		mock.ExpectRollback()
		result, err := apply(GinkgoT().Context(), db, request, func(context.Context, *sql.Tx, snapshot.Options) (snapshot.Result, error) {
			changed := testRequest().Artifact.Request.Desired
			changed.Tables[0].Columns[0].Type = "bigint"
			return snapshot.Result{Schema: changed}, nil
		})
		Expect(err).To(MatchError(ContainSubstring("baseline drift")))
		Expect(result.Outcome).To(Equal(Failed))
	})

	It("rolls back a statement failure and preserves its cause", func() {
		begin(0)
		cause := errors.New("DDL denied")
		mock.ExpectExec("sp_executesql").WithArgs(request.Artifact.Plan.Steps[0].SQL).WillReturnError(cause)
		mock.ExpectRollback()
		result, err := apply(GinkgoT().Context(), db, request, inspect)
		Expect(errors.Is(err, cause)).To(BeTrue())
		Expect(result.Outcome).To(Equal(Failed))
		Expect(inspections).To(Equal(1))
	})

	It("rolls back when successful SQL did not produce the complete desired state", func() {
		begin(0)
		mock.ExpectExec("sp_executesql").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectRollback()
		result, err := apply(GinkgoT().Context(), db, request, func(context.Context, *sql.Tx, snapshot.Options) (snapshot.Result, error) {
			return snapshot.Result{Schema: request.Artifact.Request.Current}, nil
		})
		Expect(err).To(MatchError(ContainSubstring("postcondition")))
		Expect(result.Outcome).To(Equal(Failed))
	})

	It("reports an uncertain commit separately from rollback failures", func() {
		begin(0)
		mock.ExpectExec("sp_executesql").WillReturnResult(sqlmock.NewResult(0, 0))
		cause := errors.New("connection lost while committing")
		mock.ExpectCommit().WillReturnError(cause)
		result, err := apply(GinkgoT().Context(), db, request, inspect)
		Expect(errors.Is(err, cause)).To(BeTrue())
		Expect(result.Outcome).To(Equal(CommitUnknown))
	})

	It("rejects lock failures before inspecting or changing the database", func() {
		begin(-1)
		mock.ExpectRollback()
		result, err := apply(GinkgoT().Context(), db, request, inspect)
		Expect(err).To(MatchError(ContainSubstring("sp_getapplock returned -1")))
		Expect(result.Outcome).To(Equal(Failed))
		Expect(inspections).To(BeZero())
	})

	It("rejects a changed reviewed hash before opening a transaction", func() {
		request.Hash = "changed"
		_, err := Apply(GinkgoT().Context(), db, request)
		Expect(err).To(MatchError(ContainSubstring("reviewed hash")))
	})

	It("requires each destructive change ID and rejects unknown or repeated acknowledgements", func() {
		request.Artifact.Request.Current, request.Artifact.Request.Desired = request.Artifact.Request.Desired, request.Artifact.Request.Current
		var err error
		request.Artifact, err = planner.BuildArtifact(migrate.ArtifactRequest{Schemas: request.Artifact.Schemas, Request: request.Artifact.Request})
		Expect(err).NotTo(HaveOccurred())
		request.Hash = request.Artifact.Hash
		id := request.Artifact.Plan.Changes[0].ID
		for _, acknowledgements := range [][]string{nil, {"unknown"}, {id, id}} {
			request.Acknowledgements = acknowledgements
			_, err := Apply(GinkgoT().Context(), db, request)
			Expect(err).To(MatchError(ContainSubstring("acknowledg")))
		}
		request.Acknowledgements = []string{id}
		begin(0)
		mock.ExpectExec(regexp.QuoteMeta("sp_executesql")).WithArgs(request.Artifact.Plan.Steps[0].SQL).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()
		result, err := apply(GinkgoT().Context(), db, request, inspect)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(Applied))
	})
})
