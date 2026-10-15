package mssqlcatalog_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMSSQLCatalog(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL Server Catalog Suite")
}
