package providers_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// Register the built-in providers under test.
	_ "github.com/flanksource/commons-db/query/providers"
)

func TestProviders(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Query Providers Suite")
}
