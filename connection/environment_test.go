package connection

import (
	"context"
	"os/exec"
	"path/filepath"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Connection environment setup", func() {
	It("removes credentials when a later connection setup fails", func() {
		workDir := GinkgoT().TempDir()
		GinkgoT().Setenv("PATH", "")
		cmd := exec.Command("true")
		cmd.Dir = workDir

		result, err := SetupConnection(dbcontext.NewContext(context.Background()), ExecConnections{
			AWS: &AWSConnection{
				AccessKey: types.EnvVar{ValueStatic: "cleanup-access-key"},
				SecretKey: types.EnvVar{ValueStatic: "cleanup-secret-key"},
			},
			Azure: &AzureConnection{
				ClientID:     &types.EnvVar{ValueStatic: "cleanup-client"},
				ClientSecret: &types.EnvVar{ValueStatic: "cleanup-client-secret"},
				TenantID:     "cleanup-tenant",
			},
		}, cmd)
		Expect(err).To(HaveOccurred())
		Expect(result).To(BeNil())

		credentialFiles, globErr := filepath.Glob(filepath.Join(workDir, ".creds", "cred-*", "credentials"))
		Expect(globErr).ToNot(HaveOccurred())
		Expect(credentialFiles).To(BeEmpty())
	})
})
