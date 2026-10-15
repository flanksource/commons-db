// Runs the pg_proxy trace kind's specs: a real proxy in front of a fake
// postgres, captured end to end into a real results store.

package pgproxy

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// The specs open results stores, whose profiles page through the sqlite
	// provider.
	_ "github.com/flanksource/commons-db/query/providers/sqlite"
)

func TestPgProxy(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "PG Proxy Trace Suite")
}
