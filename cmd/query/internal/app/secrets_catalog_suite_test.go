package app

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSecretsCatalog(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Secrets Catalog Suite")
}
