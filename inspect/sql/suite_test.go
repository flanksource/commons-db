package sqlinspect

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSQLInspection(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL Inspection")
}
