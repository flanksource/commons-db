// Runs the http_proxy trace kind's specs: traffic a client sends through the
// recording proxy, captured end to end into a real results store.

package httpproxy_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// The specs open results stores, whose profiles page through the sql
	// provider the providers package registers.
	_ "github.com/flanksource/commons-db/query/providers"
)

func TestHTTPProxy(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "HTTP Proxy Trace Suite")
}
