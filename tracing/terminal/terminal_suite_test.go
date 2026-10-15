package terminal

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// The specs open results stores, whose profiles page through the sqlite
	// provider.
	_ "github.com/flanksource/commons-db/query/providers/sqlite"
)

func TestTerminal(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Terminal Trace Suite")
}
