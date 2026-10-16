// Runs the stacks kind's specs: real Go processes dumped over each transport
// into a real results store.

package stacks_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// The specs open results stores, whose profiles page through the sqlite
	// provider.
	_ "github.com/flanksource/commons-db/query/providers/sqlite"
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

// goChild is the Go child binary the specs dump.
var goChild string

var _ = BeforeSuite(func() {
	goChild = tracingtest.GoChildBinary(GinkgoT().TempDir())
})

func TestStacks(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Stacks Trace Suite")
}
