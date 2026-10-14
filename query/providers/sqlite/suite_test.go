package sqlite_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// Registers only the "sqlite" key, as a record-store host would.
	_ "github.com/flanksource/commons-db/query/providers/sqlite"
)

func TestSQLite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Query SQL Provider Suite")
}
