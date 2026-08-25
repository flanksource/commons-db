package inspect_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestInspect(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Inspection Cache Suite")
}
