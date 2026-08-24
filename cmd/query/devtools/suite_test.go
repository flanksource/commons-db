package devtools_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDevtools(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Devtools Suite")
}
