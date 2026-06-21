package processor_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// Register the built-in processors under test.
	_ "github.com/flanksource/commons-db/query/processor"
)

func TestProcessor(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Query Processor Suite")
}
