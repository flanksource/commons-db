package recordresults_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// The specs page result profiles, which the providers package registers the
	// sql provider for; the library itself links none.
	_ "github.com/flanksource/commons-db/query/providers"
)

func TestRecordResults(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Record Results Suite")
}
