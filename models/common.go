package models

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/flanksource/commons/logger"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

type Health string

const (
	HealthHealthy   Health = "healthy"
	HealthUnhealthy Health = "unhealthy"
	HealthUnknown   Health = "unknown"
	HealthWarning   Health = "warning"
)

func WorseHealth(healths ...Health) Health {
	worst := HealthHealthy
	for _, h := range healths {
		switch h {
		case HealthUnhealthy:
			return HealthUnhealthy
		case HealthWarning:
			worst = HealthWarning
		}
	}
	return worst
}

func init() {
	logger.SkipFrameContains = append(logger.SkipFrameContains, "commons-db/models")
}

// asMap marshals the given struct into a map.
func asMap(t any, removeFields ...string) map[string]any {
	m := make(map[string]any)
	b, _ := json.Marshal(&t)
	if err := json.Unmarshal(b, &m); err != nil {
		return m
	}

	for _, field := range removeFields {
		delete(m, field)
	}

	return m
}

type DBTable interface {
	PK() string
	TableName() string
}

type Deleteable interface {
	Delete(db *gorm.DB) error
}

// TODO: Find a better way to handle this
type ExtendedDBTable interface {
	DBTable
	Value() any
	PKCols() []clause.Column
}

func GetIDs[T DBTable](items ...T) []string {
	var ids []string
	for _, item := range items {
		ids = append(ids, item.PK())
	}
	return ids
}

type Contextable interface {
	Context() map[string]any
}

func ErrorContext(items ...Contextable) []any {
	merged := make(map[string]any)

	for _, item := range items {
		if item == nil {
			continue
		}
		for k, v := range item.Context() {
			merged[k] = v
		}
	}
	var args []any

	for k, v := range merged {
		if v == nil || v == uuid.Nil.String() {
			continue
		}
		args = append(args, k, v)
	}
	return args
}

type LogNameAccessor interface {
	LoggerName() string
}

type NamespaceScopeAccessor interface {
	GetNamespace() string
}

// noopMatcher implements TagsMatchable
type noopMatcher struct {
}

func (t noopMatcher) Has(field string) (exists bool) {
	return false
}

func (t noopMatcher) Get(field string) (value string) {
	return ""
}

func (t noopMatcher) Lookup(field string) (value string, exists bool) {
	return "", false
}

// ABACAttribute is the object passed to authorization checks.
// Consumers can extend this with their own types.
type ABACAttribute struct {
	Connection Connection
}

type TaggableModel interface {
	GetTags() map[string]string
}

type LabelableModel interface {
	GetLabels() map[string]string
}

type ColumnType string

const (
	ColumnTypeBoolean  ColumnType = "boolean"
	ColumnTypeDateTime ColumnType = "datetime"
	ColumnTypeDecimal  ColumnType = "decimal"
	ColumnTypeDuration ColumnType = "duration"
	ColumnTypeInteger  ColumnType = "integer"
	ColumnTypeJSONB    ColumnType = "jsonb"
	ColumnTypeString   ColumnType = "string"
)

func (t ColumnType) SQLiteType() string {
	switch t {
	case ColumnTypeBoolean:
		return "BOOLEAN"
	case ColumnTypeDateTime:
		return "DATETIME"
	case ColumnTypeDecimal:
		return "DECIMAL"
	case ColumnTypeDuration:
		return "INTEGER"
	case ColumnTypeInteger:
		return "INTEGER"
	case ColumnTypeJSONB:
		return "BLOB"
	case ColumnTypeString:
		return "TEXT"
	default:
		return "TEXT"
	}
}

type AppProperty struct {
	Name      string     `json:"name,omitempty"`
	Value     string     `json:"value,omitempty"`
	CreatedBy *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt time.Time  `json:"created_at,omitempty" time_format:"postgres_timestamp" gorm:"default:CURRENT_TIMESTAMP()"`
	UpdatedAt time.Time  `json:"updated_at,omitempty" time_format:"postgres_timestamp" gorm:"default:CURRENT_TIMESTAMP()"`
	DeletedAt *time.Time `json:"deleted_at,omitempty" time_format:"postgres_timestamp" gorm:"default:CURRENT_TIMESTAMP()"`
}

func (p AppProperty) TableName() string {
	return "properties"
}

// ConvertRowToNativeTypes converts a database row to native go types
func ConvertRowToNativeTypes(row map[string]any, columnDef map[string]ColumnType) (map[string]any, map[string]string) {
	// keep track of conversion error per column
	invalidTypesPerColumn := make(map[string]string)

	for colName, value := range row {
		colType, ok := columnDef[colName]
		if !ok {
			continue
		}

		switch colType {
		case ColumnTypeJSONB:
			if raw, ok := value.([]uint8); ok {
				row[colName] = json.RawMessage(raw)
			}

		case ColumnTypeDuration:
			converted, invalid := convertToDuration(value)
			row[colName] = converted
			recordInvalidType(invalidTypesPerColumn, colName, invalid)

		case ColumnTypeDateTime:
			converted, invalid := convertToDateTime(value)
			row[colName] = converted
			recordInvalidType(invalidTypesPerColumn, colName, invalid)

		case ColumnTypeString:
			if value == nil {
				row[colName] = ""
			} else {
				row[colName] = fmt.Sprintf("%v", value)
			}

		case ColumnTypeInteger:
			if value == nil {
				row[colName] = 0
			}

		case ColumnTypeDecimal:
			if value == nil {
				row[colName] = float64(0)
			}

		case ColumnTypeBoolean:
			converted, invalid := convertToBoolean(value)
			row[colName] = converted
			recordInvalidType(invalidTypesPerColumn, colName, invalid)

		default:
			// do nothing
		}
	}

	return row, invalidTypesPerColumn
}

func recordInvalidType(invalidTypesPerColumn map[string]string, colName, invalid string) {
	if invalid == "" {
		return
	}
	if _, exists := invalidTypesPerColumn[colName]; !exists {
		invalidTypesPerColumn[colName] = invalid
	}
}

func convertToDuration(value any) (any, string) {
	switch v := value.(type) {
	case int:
		return time.Duration(v), ""
	case int32:
		return time.Duration(v), ""
	case int64:
		return time.Duration(v), ""
	case float64:
		return time.Duration(int64(v)), ""
	case string:
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Sprintf("failed to parse duration (value: %v)", v)
		}
		return parsed, ""
	case nil:
		return nil, ""
	default:
		return nil, fmt.Sprintf("invalid type %T", v)
	}
}

func convertToDateTime(value any) (any, string) {
	switch v := value.(type) {
	case time.Time:
		return v, ""
	case string:
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, fmt.Sprintf("failed to parse datetime (value: %v)", v)
		}
		return parsed, ""
	case nil:
		return nil, ""
	default:
		return nil, fmt.Sprintf("invalid type %T", v)
	}
}

func convertToBoolean(value any) (bool, string) {
	switch v := value.(type) {
	case bool:
		return v, ""
	case string:
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return false, fmt.Sprintf("failed to parse boolean (value: %v)", v)
		}
		return parsed, ""
	case int, int32, int64:
		return v != 0, ""
	case nil:
		return false, ""
	default:
		return false, fmt.Sprintf("invalid boolean type %T", v)
	}
}
