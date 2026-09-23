package db

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("NewGorm SQLite", func() {
	It("opens a bare db filepath with the SQLite dialector and managed pragmas", func() {
		database, err := NewGorm(filepath.Join(GinkgoT().TempDir(), "gorm.db"), DefaultGormConfig())
		Expect(err).ToNot(HaveOccurred())
		sqlDB, err := database.DB()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(sqlDB.Close)

		var foreignKeys, busyTimeout int
		var journalMode string
		Expect(sqlDB.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys)).To(Succeed())
		Expect(sqlDB.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout)).To(Succeed())
		Expect(sqlDB.QueryRow("PRAGMA journal_mode").Scan(&journalMode)).To(Succeed())
		Expect(map[string]any{
			"dialect":      database.Dialector.Name(),
			"foreign_keys": foreignKeys,
			"busy_timeout": busyTimeout,
			"journal_mode": journalMode,
		}).To(Equal(map[string]any{
			"dialect":      "sqlite",
			"foreign_keys": 1,
			"busy_timeout": 5000,
			"journal_mode": "wal",
		}))
	})
})
