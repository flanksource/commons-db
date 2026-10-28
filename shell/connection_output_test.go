package shell

import (
	"os/exec"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
	ginkgo "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Connection output", func() {
	ginkgo.It("preserves JSON booleans while redacting injected credentials", func() {
		dir := ginkgo.GinkgoT().TempDir()
		result, err := RunCmd(context.New(), Exec{Chroot: dir, BaseDir: dir, Connections: connection.ExecConnections{AWS: &connection.AWSConnection{AccessKey: types.EnvVar{ValueStatic: "example-access"}, SecretKey: types.EnvVar{ValueStatic: "example-secret"}}}}, exec.Command("sh", "-c", `printf '{"configured":true,"secret":"%s"}' "example-secret"`))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Stdout).To(Equal(`{"configured":true,"secret":"[REDACTED]"}`))
	})
})
