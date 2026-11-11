package main

import (
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/flanksource/commons-db/db"
	_ "github.com/flanksource/commons-db/query/processor"
	"modernc.org/sqlite"
)

type note struct {
	ID   uint `gorm:"primaryKey"`
	Body string
}

func main() {
	if err := run(); err != nil {
		panic(err)
	}
	fmt.Println("sqlite consumer passed")
}

func run() (err error) {
	if err := sqlite.RegisterDeterministicScalarFunction("consumer_label", 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			return fmt.Sprintf("label:%s", args[0]), nil
		}); err != nil {
		return err
	}
	database, err := db.NewGorm("consumer.db", nil)
	if err != nil {
		return err
	}
	connection, err := database.DB()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, connection.Close()) }()
	if err := database.AutoMigrate(&note{}); err != nil {
		return err
	}
	if err := database.Create(&note{Body: "acme"}).Error; err != nil {
		return err
	}
	var label string
	if err := database.Raw("SELECT consumer_label(body) FROM notes").Scan(&label).Error; err != nil {
		return err
	}
	if label != "label:acme" {
		return fmt.Errorf("label = %q, expected label:acme", label)
	}
	return nil
}
