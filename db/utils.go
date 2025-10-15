package db

import (
	"flag"
	"time"

	"github.com/flanksource/commons/logger"
	dbContext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/gorm"
	gormDriver "gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var DefaultQueryTimeout = 30 * time.Second

// LogLevel is the log level for gorm logger
var LogLevel string

// Table interface for models with TableName
type Table interface {
	TableName() string
}

// Now returns a SQL expression for the current timestamp
func Now() clause.Expr {
	return gormDriver.Expr("NOW()")
}

// Delete performs a soft delete on the given model
func Delete(ctx dbContext.Context, model Table) error {
	return ctx.DB().Model(model).UpdateColumn("deleted_at", Now()).Error
}

// BindGoFlags binds database flags to the provided flag set
func BindGoFlags() {
	flag.StringVar(&LogLevel, "db-log-level", "error", "Set gorm logging level. trace, debug & info")
}

// DefaultGormConfig returns the default GORM configuration
func DefaultGormConfig() *gormDriver.Config {
	return &gormDriver.Config{
		FullSaveAssociations: true,
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
		Logger: gorm.NewSqlLogger(logger.GetLogger("db")),
	}
}
