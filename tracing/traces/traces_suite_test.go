// Runs the trace plugin specs: the plugin contract, the kinds catalog and the
// runtime that drives a plugin's capture into a record store.

package traces_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// The specs open results stores, whose profiles page through the sql
	// provider the providers package registers; the library itself links none.
	_ "github.com/flanksource/commons-db/query/providers"
)

func TestTraces(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Trace Plugins Suite")
}
