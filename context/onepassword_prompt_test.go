package context

import (
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons/properties"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sync/errgroup"
)

var _ = ginkgo.Describe("1Password prompt", func() {
	const ref = "op://prod/postgres/password"
	const otherRef = "op://prod/mysql/password"

	ginkgo.BeforeEach(func() {
		ginkgo.GinkgoT().Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
		envCache.Flush()
		previous := opReadFunc
		ginkgo.DeferCleanup(func() {
			opReadFunc = previous
			envCache.Flush()
		})
	})

	ginkgo.It("runs one prompting op at a time", func() {
		dir := ginkgo.GinkgoT().TempDir()
		const exclusiveOp = "#!/bin/sh\nmkdir \"$OP_TEST_LOCK\" || exit 1\nsleep 0.1\nrmdir \"$OP_TEST_LOCK\"\nprintf ok\n"
		Expect(os.WriteFile(filepath.Join(dir, "op"), []byte(exclusiveOp), 0o700)).To(Succeed()) //nolint:gosec
		ginkgo.GinkgoT().Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		ginkgo.GinkgoT().Setenv("OP_TEST_LOCK", filepath.Join(dir, "lock"))

		var group errgroup.Group
		for _, concurrent := range []string{ref, otherRef} {
			group.Go(func() error {
				_, err := GetEnvStringFromCache(New(), concurrent, "")
				return err
			})
		}
		Expect(group.Wait()).To(Succeed())
	})

	ginkgo.DescribeTable("bounds the lookup by who answers it", func(token, property string, want time.Duration) {
		const timeoutProperty = "envvar.1password.lookup.timeout"
		ginkgo.GinkgoT().Setenv("OP_SERVICE_ACCOUNT_TOKEN", token)
		properties.Set(timeoutProperty, property)
		New().ClearCache()
		ginkgo.DeferCleanup(func() {
			properties.Set(timeoutProperty, "")
			New().ClearCache()
		})
		var left time.Duration
		opReadFunc = func(ctx Context, _, _ string) (string, error) {
			deadline, _ := ctx.Deadline()
			left = time.Until(deadline)
			return "value", nil
		}
		Expect(GetEnvStringFromCache(New(), ref, "")).To(Equal("value"))
		Expect(left).To(BeNumerically("~", want, time.Second))
	},
		ginkgo.Entry("a person approving the prompt", "", "", 2*time.Minute),
		ginkgo.Entry("a person, configured", "", "30s", 30*time.Second),
		ginkgo.Entry("a service account", "token", "", 5*time.Second),
	)
})
