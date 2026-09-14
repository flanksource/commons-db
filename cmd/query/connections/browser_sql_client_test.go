package connections

import (
	"context"
	"database/sql"
	"net/http"
	"regexp"

	"github.com/DATA-DOG/go-sqlmock"
	dbconnection "github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL browser client", func() {
	It("bootstraps an explicit SQL Server database through the login default when the configured database is unavailable", func() {
		configuredClient, configuredMock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
		Expect(err).ToNot(HaveOccurred())
		configuredMock.ExpectPing().WillReturnError(mssql.Error{
			Number:  4063,
			Message: `login error: Cannot open database "LAB_APP_QA" that was requested by the login. Using the user default database "master" instead.`,
		})
		configuredMock.ExpectClose()

		defaultClient, defaultMock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
		Expect(err).ToNot(HaveOccurred())
		defaultMock.ExpectPing()
		defaultMock.ExpectQuery(regexp.QuoteMeta(`SELECT name FROM sys.databases WHERE state = 0 AND HAS_DBACCESS(name) = 1 ORDER BY name`)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("LAB_TARGET_QA"))
		defaultMock.ExpectClose()

		selectedClient, selectedMock, err := sqlmock.New()
		Expect(err).ToNot(HaveOccurred())
		selectedMock.ExpectClose()

		handler := newConnectionBrowserHandler("", dbcontext.New(), http.NotFoundHandler())
		opened := make([]dbconnection.SQLConnection, 0, 3)
		handler.openSQLClient = func(_ context.Context, connection dbconnection.SQLConnection) (*sql.DB, error) {
			opened = append(opened, connection)
			switch len(opened) {
			case 1:
				return configuredClient, nil
			case 2:
				return defaultClient, nil
			default:
				return selectedClient, nil
			}
		}

		client, err := handler.sqlClient(context.Background(), &models.Connection{
			Type: models.ConnectionTypeSQLServer,
			URL:  "server=mssql.example;database=LAB_APP_QA;encrypt=disable",
		}, sqlClientOptions{Database: "LAB_TARGET_QA"})

		Expect(err).ToNot(HaveOccurred())
		Expect(client).To(Equal(selectedClient))
		Expect(client.Close()).To(Succeed())
		Expect(opened).To(HaveLen(3))
		configured, err := msdsn.Parse(opened[0].URL.ValueStatic)
		Expect(err).ToNot(HaveOccurred())
		fallback, err := msdsn.Parse(opened[1].URL.ValueStatic)
		Expect(err).ToNot(HaveOccurred())
		selected, err := msdsn.Parse(opened[2].URL.ValueStatic)
		Expect(err).ToNot(HaveOccurred())
		Expect(configured.Database).To(Equal("LAB_APP_QA"))
		Expect(fallback.Database).To(BeEmpty())
		Expect(selected.Database).To(Equal("LAB_TARGET_QA"))
		Expect(configuredMock.ExpectationsWereMet()).To(Succeed())
		Expect(defaultMock.ExpectationsWereMet()).To(Succeed())
		Expect(selectedMock.ExpectationsWereMet()).To(Succeed())
	})

	It("opens the SQL Server default database when catalog login cannot open the configured database", func() {
		configuredClient, configuredMock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
		Expect(err).ToNot(HaveOccurred())
		configuredMock.ExpectPing().WillReturnError(mssql.Error{
			Number:  4063,
			Message: `login error: Cannot open database "LAB_APP_QA" that was requested by the login. Using the user default database "master" instead.`,
		})
		configuredMock.ExpectClose()

		defaultClient, defaultMock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
		Expect(err).ToNot(HaveOccurred())
		defaultMock.ExpectPing()
		defaultMock.ExpectClose()

		handler := newConnectionBrowserHandler("", dbcontext.New(), http.NotFoundHandler())
		opened := make([]dbconnection.SQLConnection, 0, 2)
		handler.openSQLClient = func(_ context.Context, connection dbconnection.SQLConnection) (*sql.DB, error) {
			opened = append(opened, connection)
			if len(opened) == 1 {
				return configuredClient, nil
			}
			return defaultClient, nil
		}
		connection := &models.Connection{
			Type: models.ConnectionTypeSQLServer,
			URL:  "server=mssql.example;database=LAB_APP_QA;encrypt=disable",
		}

		client, err := handler.sqlClient(context.Background(), connection, sqlClientOptions{
			UseDefaultDatabaseOnUnavailable: true,
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(client).To(Equal(defaultClient))
		Expect(client.Close()).To(Succeed())
		Expect(opened).To(HaveLen(2))
		configured, err := msdsn.Parse(opened[0].URL.ValueStatic)
		Expect(err).ToNot(HaveOccurred())
		fallback, err := msdsn.Parse(opened[1].URL.ValueStatic)
		Expect(err).ToNot(HaveOccurred())
		Expect(configured.Database).To(Equal("LAB_APP_QA"))
		Expect(fallback.Database).To(BeEmpty())
		Expect(fallback.Host).To(Equal(configured.Host))
		Expect(configuredMock.ExpectationsWereMet()).To(Succeed())
		Expect(defaultMock.ExpectationsWereMet()).To(Succeed())
	})

	It("preserves SQL Server login failures other than an unavailable database", func() {
		client, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
		Expect(err).ToNot(HaveOccurred())
		mock.ExpectPing().WillReturnError(mssql.Error{Number: 18456, Message: "login failed for user"})
		mock.ExpectClose()

		handler := newConnectionBrowserHandler("", dbcontext.New(), http.NotFoundHandler())
		opened := 0
		handler.openSQLClient = func(_ context.Context, _ dbconnection.SQLConnection) (*sql.DB, error) {
			opened++
			return client, nil
		}

		_, err = handler.sqlClient(context.Background(), &models.Connection{
			Type: models.ConnectionTypeSQLServer,
			URL:  "server=mssql.example;database=LAB_APP_QA;encrypt=disable",
		}, sqlClientOptions{UseDefaultDatabaseOnUnavailable: true})

		Expect(err).To(MatchError(ContainSubstring("login failed for user")))
		Expect(opened).To(Equal(1))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})

	It("keeps separately resolved credentials while changing the catalog login database", func() {
		connection := dbconnection.SQLConnection{
			Type:     models.ConnectionTypeSQLServer,
			URL:      types.EnvVar{ValueStatic: "server=mssql.example;database=LAB_APP_QA;encrypt=disable"},
			Username: types.EnvVar{ValueStatic: "catalog-reader"},
			Password: types.EnvVar{ValueStatic: "secret://sqlserver/password"},
		}

		fallback, err := connection.UseDefaultDatabase()

		Expect(err).ToNot(HaveOccurred())
		Expect(fallback.Username).To(Equal(connection.Username))
		Expect(fallback.Password).To(Equal(connection.Password))
	})
})
