package dbtest

import (
	"bytes"
	"time"

	commonsdb "github.com/flanksource/commons-db/db"
	commonsgorm "github.com/flanksource/commons-db/gorm"
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("NewGorm PostgreSQL", Label("integration"), func() {
	It("installs the commons-db slow SQL logger when no config is supplied", func() {
		handle := ForGinkgo(Options{Name: "postgres_slow_query_default"})
		database, err := commonsdb.NewGorm(handle.DSN(), nil)
		Expect(err).ToNot(HaveOccurred())
		sqlDB, err := database.DB()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(sqlDB.Close)

		log, ok := database.Logger.(*commonsgorm.SqlLogger)
		Expect(ok).To(BeTrue())
		Expect(log.SlowThreshold).To(Equal(time.Second))
	})

	It("logs statements that exceed the slow threshold", func() {
		handle := ForGinkgo(Options{Name: "postgres_slow_query_log"})
		var output bytes.Buffer
		log := logger.NewWithWriter(&output)
		log.SetLogLevel(logger.Warn)
		sqlLog := commonsgorm.NewSqlLogger(log).(*commonsgorm.SqlLogger)
		sqlLog.SlowThreshold = time.Nanosecond
		config := commonsdb.DefaultGormConfig()
		config.Logger = sqlLog
		database, err := commonsdb.NewGorm(handle.DSN(), config)
		Expect(err).ToNot(HaveOccurred())
		sqlDB, err := database.DB()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(sqlDB.Close)

		var result int
		Expect(database.Raw("SELECT 1").Scan(&result).Error).To(Succeed())
		Expect(result).To(Equal(1))
		Expect(output.String()).To(And(ContainSubstring("SLOW SQL"), ContainSubstring("SELECT 1")))
	})
})
