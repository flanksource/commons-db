// Ginkgo suite for the owner package: store election, the state file, the
// control socket, spooled writes, ingestion and promotion.
package owner_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestOwner(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Record Store Owner Suite")
}
