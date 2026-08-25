package connections

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestConnectionsDashboard(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Connections dashboard")
}
