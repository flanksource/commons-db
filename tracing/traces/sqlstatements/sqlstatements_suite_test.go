// Runs the sql trace kind's specs: statements commons-db runs captured end to
// end into a real results store.

package sqlstatements_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// The specs open results stores, whose profiles page through the sql
	// provider the providers package registers.
	_ "github.com/flanksource/commons-db/query/providers"
)

func TestSQLStatements(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL Statements Trace Suite")
}
