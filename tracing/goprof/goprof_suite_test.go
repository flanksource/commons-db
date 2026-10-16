// Runs the goprof kind's specs: real Go profiles read into a real results
// store and exported back.

package goprof_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// The specs open results stores, whose profiles page through the sqlite
	// provider.
	_ "github.com/flanksource/commons-db/query/providers/sqlite"
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

// goChild is the Go child binary the specs profile.
var goChild string

var _ = BeforeSuite(func() {
	goChild = tracingtest.GoChildBinary(GinkgoT().TempDir())
})

func TestGoprof(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Goprof Trace Suite")
}
