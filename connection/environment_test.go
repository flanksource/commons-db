package connection

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Connection environment setup", func() {
	for _, provider := range []string{"azure", "gcp"} {
		It("isolates "+provider+" CLI authentication and removes its cache", func() {
			dir := GinkgoT().TempDir()
			bin := filepath.Join(dir, "bin")
			Expect(os.MkdirAll(bin, 0700)).To(Succeed())
			tool, variable := "az", "AZURE_CONFIG_DIR"
			connections := ExecConnections{Azure: &AzureConnection{ClientID: &types.EnvVar{ValueStatic: "client"}, ClientSecret: &types.EnvVar{ValueStatic: "secret"}, TenantID: "tenant"}}
			if provider == "gcp" {
				tool, variable = "gcloud", "CLOUDSDK_CONFIG"
				connections = ExecConnections{GCP: &GCPConnection{Credentials: &types.EnvVar{ValueStatic: `{"type":"service_account"}`}}}
			}
			Expect(os.WriteFile(filepath.Join(bin, tool), []byte("#!/bin/sh\ntest -n \"$"+variable+"\" || exit 9\nprintf '%s' \"$"+variable+"\" > \"$PROJECTION_CACHE_RECORD\"\n"), 0700)).To(Succeed())
			GinkgoT().Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			record := filepath.Join(dir, "cache-path")
			cmd := exec.Command("true")
			cmd.Dir, cmd.Env = dir, append(os.Environ(), "PROJECTION_CACHE_RECORD="+record)
			result, err := SetupConnection(dbcontext.New(), connections, cmd)
			Expect(err).NotTo(HaveOccurred())
			cache, err := os.ReadFile(record)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(cache)).To(BeADirectory())
			Expect(cmd.Env).To(ContainElement(variable + "=" + string(cache)))
			Expect(result.Cleanup()).To(Succeed())
			Expect(string(cache)).NotTo(BeAnExistingFile())
		})
	}
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
