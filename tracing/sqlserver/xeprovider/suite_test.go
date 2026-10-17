package xeprovider

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestXEProvider(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL Server XE Provider Suite")
}
