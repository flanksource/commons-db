package viewdeps

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"testing"
)

func TestCompatibilityViews(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "View dependency compatibility")
}
