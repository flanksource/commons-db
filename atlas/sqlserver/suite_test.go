package sqlserver_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSQLServerDriver(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Atlas SQL Server Driver Suite")
}
