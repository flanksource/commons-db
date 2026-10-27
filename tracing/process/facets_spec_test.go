package process

import (
	"context"
	"os"
	"syscall"

	clickyprocess "github.com/flanksource/clicky/process"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("samplesRun.read", func() {
	It("reads a tick whose tree holds a process it may not read, keeping why on that process", func() {
		Expect(os.Geteuid()).NotTo(BeZero(), "pid 1 is readable as root, so this cannot observe EPERM")
		const rootOwned = 1
		run := &samplesRun{params: SampleParams{}}
		listed := []observed{
			{Process: clickyprocess.Process{PID: os.Getpid(), Status: "active", Active: true}},
			{Process: clickyprocess.Process{PID: rootOwned, Status: "sleeping", Active: true}},
		}

		readings, _, err := run.read(context.Background(), listed)

		Expect(err).NotTo(HaveOccurred())
		Expect(readings[os.Getpid()].IO).NotTo(BeNil())
		Expect(readings[rootOwned].IO).To(BeNil())
		Expect(readings[rootOwned].IOError).To(SatisfyAny(
			ContainSubstring(syscall.EPERM.Error()),
			ContainSubstring(syscall.EACCES.Error()),
		))
	})
})
