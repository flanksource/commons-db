package sqlite_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSQLiteMigrate(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQLite Migrate Suite")
}
