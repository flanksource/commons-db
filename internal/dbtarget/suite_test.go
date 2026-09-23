package dbtarget_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDatabaseTarget(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Database Target Suite")
}
