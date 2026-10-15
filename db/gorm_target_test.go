package db

import (
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

var _ = Describe("NewGorm SQL Server", func() {
	It("opens a sqlserver:// DSN with the SQL Server dialector and driver", func() {
		database, err := NewGorm("sqlserver://sa:secret@127.0.0.1:1?database=app", &gorm.Config{DisableAutomaticPing: true})
		Expect(err).ToNot(HaveOccurred())
		sqlDB, err := database.DB()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(sqlDB.Close)

		Expect(database.Dialector.Name()).To(Equal("sqlserver"))
		Expect(sqlDB.Stats().MaxOpenConnections).To(Equal(DefaultPoolConfig().MaxOpenConns))
	})

	// Set COMMONS_DB_SQLSERVER_URL to run: sqlserver://sa:secret@localhost:1433?database=master
	It("runs parameterized queries against a live SQL Server", func() {
		address := strings.TrimSpace(os.Getenv("COMMONS_DB_SQLSERVER_URL"))
		if address == "" {
			Skip("COMMONS_DB_SQLSERVER_URL is not set")
		}
		database, err := NewGorm(address, nil)
		Expect(err).ToNot(HaveOccurred())
		sqlDB, err := database.DB()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(sqlDB.Close)

		var echoed string
		Expect(database.Raw("SELECT ?", "commons-db").Scan(&echoed).Error).To(Succeed())
		Expect(echoed).To(Equal("commons-db"))
	})
})
