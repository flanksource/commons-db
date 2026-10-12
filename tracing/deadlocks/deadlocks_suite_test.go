package deadlocks_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDeadlocksSpecs(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "deadlocks suite")
}

// fixture reads a deadlock report captured from a system_health session: the
// key-lookup-two-process, -three-victims and -seven-victims reports come from the
// lab on 2026-09-10, during the AsActivity index benchmark; the ix2 report from
// a UAT environment on 2026-09-14, after its AsActivity recluster; the
// module-frame report, whose sys.sp_executesql frames carry stmtstart="-1",
// from the lab on 2026-10-01 (hosts, logins and databases renamed).
func fixture(name string) string {
	GinkgoHelper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	Expect(err).NotTo(HaveOccurred())
	return string(data)
}
