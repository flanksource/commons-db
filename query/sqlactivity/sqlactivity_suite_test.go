package sqlactivity

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSQLActivity(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL activity")
}
