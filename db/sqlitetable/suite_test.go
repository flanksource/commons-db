package sqlitetable_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSQLiteTable(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQLite Table Suite")
}
