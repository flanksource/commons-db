package esdsl

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestESDSL(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ESDSL")
}
