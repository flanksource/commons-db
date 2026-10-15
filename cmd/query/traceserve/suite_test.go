package traceserve_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTraceServe(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Trace Serve Suite")
}
