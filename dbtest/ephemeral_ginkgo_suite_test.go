package dbtest

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestEphemeralDatabases(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Ephemeral Database Suite")
}
