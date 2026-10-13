// Runs the jfr trace kind's specs: recordings decoded from fixture files, and
// captures through a fake recorder into a real results store.

package jfr_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	// The specs open results stores, whose profiles page through the sql
	// provider the providers package registers.
	_ "github.com/flanksource/commons-db/query/providers"
)

func TestJFR(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "JFR Trace Suite")
}

// fixture is the recording testdata/<name>.jfr.gz holds, decompressed.
func fixture(name string) []byte {
	compressed, err := os.ReadFile(filepath.Join("testdata", name+".jfr.gz"))
	Expect(err).ToNot(HaveOccurred())
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	Expect(err).ToNot(HaveOccurred())
	data, err := io.ReadAll(reader)
	Expect(err).ToNot(HaveOccurred())
	return data
}
