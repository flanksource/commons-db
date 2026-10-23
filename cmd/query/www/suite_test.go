package www_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestWWW(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "query app")
}
