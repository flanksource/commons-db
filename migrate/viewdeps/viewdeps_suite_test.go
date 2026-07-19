package viewdeps

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestViewdeps(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "viewdeps")
}
