// Runs the process trace kinds' specs: real process trees captured end to end
// into a real results store.

package process_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// The specs open results stores, whose profiles page through the sqlite
	// provider.
	_ "github.com/flanksource/commons-db/query/providers/sqlite"
)

func TestProcess(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Process Trace Suite")
}
