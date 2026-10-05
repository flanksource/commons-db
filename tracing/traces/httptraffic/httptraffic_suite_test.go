// Runs the http trace kind's specs: outbound exchanges of observed features
// captured end to end into a real results store.

package httptraffic_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// The specs open results stores, whose profiles page through the sql
	// provider the providers package registers.
	_ "github.com/flanksource/commons-db/query/providers"
)

func TestHTTPTraffic(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "HTTP Traffic Trace Suite")
}
