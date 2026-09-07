package senders_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSenders(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Notification Senders Suite")
}
