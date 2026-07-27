package db

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestEmbeddedConfiguration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Embedded PostgreSQL Configuration Suite")
}
