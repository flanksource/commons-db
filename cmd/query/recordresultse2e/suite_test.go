// Suite for the record result specs that need the query CLI's HTTP surface:
// the profile service and the sessions API serving a result registry.
package recordresultse2e

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	_ "github.com/flanksource/commons-db/query/providers"
)

func TestRecordResultsE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Record Results E2E Suite")
}
