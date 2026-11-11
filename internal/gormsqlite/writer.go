package sqlite

import (
	"fmt"

	"gorm.io/gorm/clause"
)

func writeString(writer clause.Writer, value string) {
	if _, err := writer.WriteString(value); err != nil {
		panic(fmt.Errorf("write SQLite SQL: %w", err))
	}
}

func writeByte(writer clause.Writer, value byte) {
	if err := writer.WriteByte(value); err != nil {
		panic(fmt.Errorf("write SQLite SQL: %w", err))
	}
}
