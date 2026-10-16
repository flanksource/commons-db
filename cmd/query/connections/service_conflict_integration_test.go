package connections

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"

	"github.com/flanksource/clicky/entity"
	dbcontext "github.com/flanksource/commons-db/context"
	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

const dependentConstraint = "database_modules_connection_id_fkey"

// sqliteConflict is the refusal on SQLite, which reports neither the constraint
// nor the table, so the error names none.
var sqliteConflict = &entity.StatusError{
	Status:  http.StatusConflict,
	Code:    connectionInUseCode,
	Message: `cannot delete connection "warehouse": a foreign key constraint refuses it`,
}

func openPostgresConflictDB() *gorm.DB {
	return dbtest.ForGinkgo(dbtest.Options{Name: "connections_fk_conflict"}).Gorm()
}

// openSQLiteConflictDB opens SQLite the way a host does, through NewGorm, which
// turns foreign-key enforcement on for every connection.
func openSQLiteConflictDB() *gorm.DB {
	database, err := commonsdb.NewGorm("sqlite://"+filepath.Join(GinkgoT().TempDir(), "connections.db"), nil)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		sqlDB, err := database.DB()
		Expect(err).NotTo(HaveOccurred())
		Expect(sqlDB.Close()).To(Succeed())
	})
	return database
}

// createReferencedConnectionTables builds a connections table plus a host table
// whose rows reference a connection through a foreign key, the shape a
// downstream app uses to keep a connection that is still in use. The DDL is
// valid on both PostgreSQL and SQLite. onDelete matters on SQLite: RESTRICT
// fails as SQLITE_CONSTRAINT_TRIGGER, NO ACTION as SQLITE_CONSTRAINT_FOREIGNKEY.
func createReferencedConnectionTables(database *gorm.DB, onDelete string) {
	Expect(database.Exec(`CREATE TABLE connections (
        id text PRIMARY KEY, name text NOT NULL UNIQUE, namespace text, source text, type text NOT NULL,
        url text, username text, password text, properties jsonb, certificate text,
        insecure_tls boolean DEFAULT false, created_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
        updated_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP, created_by text
    )`).Error).To(Succeed())
	Expect(database.Exec(`CREATE TABLE database_modules (
        id text PRIMARY KEY,
        connection_id text NOT NULL CONSTRAINT ` + dependentConstraint + `
            REFERENCES connections(id) ON DELETE ` + onDelete + `
    )`).Error).To(Succeed())
}

func newConflictTestService(database *gorm.DB) *Service {
	ctx := dbcontext.NewContext(context.Background()).WithDB(database, nil)
	service, err := New(Options{
		Database:   func() (*gorm.DB, error) { return database, nil },
		Context:    func() dbcontext.Context { return ctx },
		DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
		Profiles:   func(context.Context) ([]query.Profile, error) { return nil, nil },
	})
	Expect(err).NotTo(HaveOccurred())
	return service
}

var _ = Describe("deleting a connection a host row still references", func() {
	DescribeTable("is refused as a 409 connection_in_use and leaves the connection in place",
		func(open func() *gorm.DB, onDelete string, expected *entity.StatusError) {
			database := open()
			createReferencedConnectionTables(database, onDelete)
			connection := models.Connection{
				ID: uuid.New(), Name: "warehouse", Type: models.ConnectionTypePostgres, URL: "postgres://warehouse.example/app",
			}
			Expect(database.Create(&connection).Error).To(Succeed())
			Expect(database.Exec(`INSERT INTO database_modules (id, connection_id) VALUES ('orders', ?)`,
				connection.ID.String()).Error).To(Succeed())

			err := newConflictTestService(database).Delete(connection.Name)

			var status *entity.StatusError
			Expect(errors.As(err, &status)).To(BeTrue(), "expected a classified StatusError, got %v", err)
			Expect(status).To(Equal(expected))

			recorder := httptest.NewRecorder()
			entity.WriteError(recorder, err)
			var body map[string]any
			Expect(json.Unmarshal(recorder.Body.Bytes(), &body)).To(Succeed())
			Expect(recorder.Code).To(Equal(http.StatusConflict))
			Expect(body).To(HaveKeyWithValue("code", connectionInUseCode))

			var remaining int64
			Expect(database.Model(&models.Connection{}).Where("id = ?", connection.ID.String()).Count(&remaining).Error).To(Succeed())
			Expect(remaining).To(Equal(int64(1)))
		},
		Entry("PostgreSQL names the constraint and the referencing table", openPostgresConflictDB, "RESTRICT", &entity.StatusError{
			Status:  http.StatusConflict,
			Code:    connectionInUseCode,
			Message: `cannot delete connection "warehouse": foreign key constraint ` + dependentConstraint + ` on table database_modules refuses it`,
			Context: map[string]any{"constraint": dependentConstraint, "table": "database_modules"},
		}),
		Entry("SQLite RESTRICT names neither", openSQLiteConflictDB, "RESTRICT", sqliteConflict),
		Entry("SQLite NO ACTION names neither", openSQLiteConflictDB, "NO ACTION", sqliteConflict),
	)
})
