package snapshots_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSnapshots(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Reconciliation Snapshots Suite")
}
