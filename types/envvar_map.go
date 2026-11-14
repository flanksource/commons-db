package types

import (
	"database/sql/driver"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// EnvVarMap is a set of named EnvVars stored as one JSON column, each a value
// or a valueFrom reference.
type EnvVarMap map[string]EnvVar

// Value implements driver.Valuer; an empty map is stored as NULL.
func (m EnvVarMap) Value() (driver.Value, error) {
	if len(m) == 0 {
		return nil, nil
	}
	return GenericStructValue(m, false)
}

// Scan implements sql.Scanner.
func (m *EnvVarMap) Scan(val any) error {
	return GenericStructScan(&m, val)
}

func (EnvVarMap) GormDataType() string {
	return "envvarmap"
}

func (EnvVarMap) GormDBDataType(db *gorm.DB, _ *schema.Field) string {
	return JSONGormDBDataType(db.Dialector.Name())
}
