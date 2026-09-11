package recordresults_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRecordResults(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Record Results Suite")
}
