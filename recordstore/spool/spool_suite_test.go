// Ginkgo suite for the spool: manifests, codecs, publishing, loading, the
// bulk writer and garbage collection.
package spool_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSpool(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Record Store Spool Suite")
}
