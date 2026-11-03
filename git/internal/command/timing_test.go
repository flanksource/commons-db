package command

import (
	"context"
	"testing"

	rpchttp "github.com/flanksource/clicky/rpc/http"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCommandTiming(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Git command timing") }

var _ = Describe("raw command timing", func() {
	It("accounts for each raw subprocess exactly once, including failures", func() {
		ctx, timings := rpchttp.WithTimings(context.Background())
		_, err := Run(ctx, Options{Args: []string{"version"}})
		Expect(err).NotTo(HaveOccurred())
		_, err = Run(ctx, Options{Args: []string{"rev-parse", "--not-a-real-option"}, Directory: GinkgoT().TempDir()})
		Expect(err).To(HaveOccurred())
		ops, _ := timings.Counter("git", "commands")
		failures, _ := timings.Counter("git", "errors")
		Expect([]int64{ops, failures}).To(Equal([]int64{2, 1}))
	})
})
