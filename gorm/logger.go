package gorm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	commons "github.com/flanksource/commons/logger"
	"github.com/flanksource/commons/properties"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// LogLevel log level
type LogLevel int

const (
	// Silent silent log level
	Silent LogLevel = iota + 1
	// Error error log level
	Error
	// Warn warn log level
	Warn
	// Info info log level
	Info
)

const (
	Reset       = "\033[0m"
	Red         = "\033[31m"
	Green       = "\033[32m"
	Yellow      = "\033[33m"
	Blue        = "\033[34m"
	Magenta     = "\033[35m"
	Cyan        = "\033[36m"
	White       = "\033[37m"
	BlueBold    = "\033[34;1m"
	MagentaBold = "\033[35;1m"
	RedBold     = "\033[31;1m"
	YellowBold  = "\033[33;1m"
)

type Logger interface {
	LogMode(LogLevel) logger.Interface
	Info(context.Context, string, ...interface{})
	Warn(context.Context, string, ...interface{})
	Error(context.Context, string, ...interface{})
	Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error)
}

type Config struct {
	SlowThreshold             time.Duration
	Colorful                  bool
	IgnoreRecordNotFoundError bool
	LogLevel                  int
}

type SqlLogger struct {
	Config
	commons.Logger
	traceParams            bool
	maxLength              int
	baseLevel              commons.LogLevel
	loggerName             string
	defaultStatementOffset bool
}

type schemaChangeContextKey struct{}

// SchemaChangeSession returns a GORM session whose DDL is treated as an
// intentional schema change for SQL log classification.
func SchemaChangeSession(db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	ctx := context.Background()
	if db.Statement != nil && db.Statement.Context != nil {
		ctx = db.Statement.Context
	}
	return db.Session(&gorm.Session{Context: context.WithValue(ctx, schemaChangeContextKey{}, true)})
}

func isSchemaChange(ctx context.Context) bool {
	v, _ := ctx.Value(schemaChangeContextKey{}).(bool)
	return v
}

func (l *SqlLogger) WithLogLevel(level any) *SqlLogger {
	newlogger := *l
	newlogger.Logger = l.Logger.WithV(level)
	newlogger.defaultStatementOffset = false
	return &newlogger
}

func (l *SqlLogger) WithLogger(name string, level any) *SqlLogger {
	newlogger := *l
	newlogger.Logger = commons.GetLogger(name)
	newlogger.baseLevel = commons.ParseLevel(l.Logger, level)
	newlogger.loggerName = name
	newlogger.defaultStatementOffset = false
	return &newlogger
}

func FromCommonsLevel(l commons.Logger, level any) logger.LogLevel {
	return logger.LogLevel(commons.ParseLevel(l, level))
}

func (l *SqlLogger) LogMode(level logger.LogLevel) logger.Interface {
	return l.WithLogLevel(level)
}

func NewSqlLogger(logger *commons.SlogLogger) logger.Interface {
	return &SqlLogger{
		Config: Config{
			Colorful:                  true,
			SlowThreshold:             properties.Duration(time.Second, "log.db.slowThreshold"),
			IgnoreRecordNotFoundError: true,
		},
		Logger:                 logger,
		traceParams:            logger.IsTraceEnabled() || properties.On(false, "log.db.params"),
		maxLength:              properties.Int(1024, "log.db.maxLength"),
		baseLevel:              commons.Info,
		loggerName:             logger.Prefix,
		defaultStatementOffset: true,
	}
}

func (s SqlLogger) Warn(ctx context.Context, format string, args ...interface{}) {
	s.Warnf(format, args...)
}

func (s SqlLogger) Info(ctx context.Context, format string, args ...interface{}) {
	s.Infof(format, args...)
}
func (s SqlLogger) Error(ctx context.Context, format string, args ...interface{}) {
	s.Errorf(format, args...)
}

var detailsFmt = Yellow + "[%dms] " + BlueBold + "[rows:%v]" + Reset + " %s"

// Trace print sql message
//
//nolint:cyclop
func (l *SqlLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if !l.IsLevelEnabled(commons.Error) {
		return
	}

	elapsed := time.Since(begin)
	msg := ""
	level := l.baseLevel

	switch {
	case err != nil && (!errors.Is(err, gorm.ErrRecordNotFound) || !l.IgnoreRecordNotFoundError):
		sql, rows := fc()
		sql = trunc(sql, l.maxLength)
		msg = fmt.Sprintf("ERROR >="+detailsFmt, elapsed/1e6, rows, err.Error()+" "+sql)
		level = l.baseLevel - (commons.Error * -1)

	case elapsed > l.SlowThreshold && l.SlowThreshold != 0:
		sql, rows := fc()
		sql = trunc(sql, l.maxLength)
		msg = fmt.Sprintf("SLOW SQL >= "+detailsFmt, elapsed/1e6, rows, sql)
		level = l.baseLevel - (commons.Warn * -1)

	case l.LogLevel == int(commons.Info):
		sql, rows := fc()
		sql = trunc(sql, l.maxLength)
		level = classifySQLLevel(sql, rows, l.baseLevel, isSchemaChange(ctx))
		level += l.statementLevelOffset()
		msg = fmt.Sprintf(detailsFmt, elapsed/1e6, rows, sql)
	}
	if l.IsLevelEnabled(level) {
		l.V(level).Infof(msg)
	}
}

func (l *SqlLogger) statementLevelOffset() commons.LogLevel {
	return statementLevelOffset(l.loggerName, l.defaultStatementOffset, properties.Get)
}

func statementLevelOffset(loggerName string, defaultStatementOffset bool, get func(string) string) commons.LogLevel {
	if !defaultStatementOffset || hasExplicitSQLLogLevel(loggerName, get) {
		return 0
	}
	return commons.Trace
}

func hasExplicitSQLLogLevel(loggerName string, get func(string) string) bool {
	if loggerName == "" {
		return false
	}
	if get("log.level."+loggerName) != "" {
		return true
	}
	return loggerName == "db" && get("db.log.level") != ""
}

func classifySQLLevel(sql string, rows int64, baseLevel commons.LogLevel, schemaChange bool) commons.LogLevel {
	verb := firstSQLVerb(sql)
	switch verb {
	case "insert", "update", "delete":
		if rows > 0 {
			return baseLevel
		}
		return baseLevel + commons.Debug
	case "truncate":
		return baseLevel
	case "create", "alter", "drop":
		if schemaChange {
			return baseLevel
		}
		return baseLevel + commons.Debug
	default:
		return baseLevel + commons.Debug
	}
}

func firstSQLVerb(sql string) string {
	fields := strings.Fields(strings.TrimSpace(sql))
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[0])
}

func trunc(s string, length int) string {
	if len(s) <= length {
		return s
	}
	return s[0:length]
}

// ParamsFilter filter params
func (l *SqlLogger) ParamsFilter(ctx context.Context, sql string, params ...interface{}) (string, []interface{}) {
	if l.traceParams || l.GetLevel() >= commons.Trace1 {
		return sql, params
	}
	return sql, nil
}
