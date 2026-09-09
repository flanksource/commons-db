package schedules_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSchedules(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Schedules Suite")
}
