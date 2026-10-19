// Runs the sql_xevent trace kind's specs: Extended Events captured through a
// fake XE session into a real results store.

package xevent_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// The specs open results stores, whose profiles page through the sql
	// provider the providers package registers.
	_ "github.com/flanksource/commons-db/query/providers"
)

func TestXEvent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL XEvent Trace Suite")
}
