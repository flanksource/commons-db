// Runs the query trace kind's specs: saved and in-place profile reads, once,
// at stop and every interval, into a real results store.

package query_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestQueryKind(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Query Trace Kind Suite")
}
