package db

import (
	"bytes"
	"path/filepath"
	"time"

	commonsgorm "github.com/flanksource/commons-db/gorm"
	"github.com/flanksource/commons/logger"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

var _ = Describe("NewGorm SQLite", func() {
	It("installs the commons-db slow SQL logger when no config is supplied", func() {
		database, err := NewGorm(filepath.Join(GinkgoT().TempDir(), "slow-log.db"), nil)
		Expect(err).ToNot(HaveOccurred())
		sqlDB, err := database.DB()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(sqlDB.Close)

		log, ok := database.Logger.(*commonsgorm.SqlLogger)
		Expect(ok).To(BeTrue())
		Expect(log.SlowThreshold).To(Equal(time.Second))
	})
	It("installs the commons-db slow SQL logger when the config has no logger", func() {
		database, err := NewGorm(filepath.Join(GinkgoT().TempDir(), "empty-config.db"), &gorm.Config{})
		Expect(err).ToNot(HaveOccurred())
		sqlDB, err := database.DB()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(sqlDB.Close)

		_, ok := database.Logger.(*commonsgorm.SqlLogger)
		Expect(ok).To(BeTrue())
	})
	It("logs SQLite statements that exceed the slow threshold", func() {
		var output bytes.Buffer
		log := logger.NewWithWriter(&output)
		log.SetLogLevel(logger.Warn)
		sqlLog := commonsgorm.NewSqlLogger(log).(*commonsgorm.SqlLogger)
		sqlLog.SlowThreshold = time.Nanosecond
		config := DefaultGormConfig()
		config.Logger = sqlLog
		database, err := NewGorm(filepath.Join(GinkgoT().TempDir(), "slow-query.db"), config)
		Expect(err).ToNot(HaveOccurred())
		sqlDB, err := database.DB()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(sqlDB.Close)

		Expect(database.Exec("CREATE TABLE slow_query_example (id INTEGER)").Error).To(Succeed())
		output.Reset()
		var count int
		Expect(database.Raw("SELECT COUNT(*) FROM slow_query_example").Scan(&count).Error).To(Succeed())
		Expect(output.String()).To(And(ContainSubstring("SLOW SQL"), ContainSubstring("SELECT COUNT(*) FROM slow_query_example")))
	})

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
