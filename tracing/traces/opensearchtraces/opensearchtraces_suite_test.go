// Runs the opensearch trace kind's specs: span documents imported and followed
// from a stub OpenSearch index into a real results store.

package opensearchtraces_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// The specs open results stores, whose profiles page through the sql
	// provider the providers package registers.
	_ "github.com/flanksource/commons-db/query/providers"
)

func TestOpenSearchTraces(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "OpenSearch Traces Suite")
}
