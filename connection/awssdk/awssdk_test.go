package awssdk_test

import (
	gocontext "context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	signerv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/connection/awssdk"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
	"github.com/flanksource/commons/http/middlewares/awssigv4"
)

func TestAWSSDK(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "connection/awssdk")
}

var _ = Describe("awssdk adapter", func() {
	// useProfile points the SDK at a shared-config profile, the credential
	// source the built-in chain cannot read.
	useProfile := func() {
		dir := GinkgoT().TempDir()
		credentials := filepath.Join(dir, "credentials")
		Expect(os.WriteFile(credentials, []byte("[example]\naws_access_key_id = profile-access\naws_secret_access_key = profile-secret\naws_session_token = profile-token\n"), 0o600)).To(Succeed())
		configFile := filepath.Join(dir, "config")
		Expect(os.WriteFile(configFile, []byte("[profile example]\nregion = ap-southeast-2\n"), 0o600)).To(Succeed())
		for name, value := range map[string]string{
			"AWS_PROFILE": "example", "AWS_SHARED_CREDENTIALS_FILE": credentials, "AWS_CONFIG_FILE": configFile,
			"AWS_ACCESS_KEY_ID": "", "AWS_SECRET_ACCESS_KEY": "", "AWS_SESSION_TOKEN": "", "AWS_REGION": "", "AWS_DEFAULT_REGION": "",
			"AWS_EC2_METADATA_DISABLED": "true",
		} {
			GinkgoT().Setenv(name, value)
		}
	}

	It("resolves shared-profile credentials and region once Install has registered the hook", func(ctx SpecContext) {
		useProfile()
		awssdk.Install()
		DeferCleanup(func() { connection.RegisterAWSCredentialsHook(nil) })

		session, err := (&connection.AWSConnection{}).Client(context.New())
		Expect(err).ToNot(HaveOccurred())
		creds, err := session.Credentials().Retrieve(ctx)
		Expect(err).ToNot(HaveOccurred())

		Expect(creds).To(Equal(awssigv4.Credentials{AccessKeyID: "profile-access", SecretAccessKey: "profile-secret", SessionToken: "profile-token"}))
		Expect(session.Region()).To(Equal("ap-southeast-2"))
	})

	It("returns an SDK config carrying the connection's static credentials and region", func(ctx SpecContext) {
		useProfile()
		conn := &connection.AWSConnection{
			Region:    "eu-west-1",
			AccessKey: types.EnvVar{ValueStatic: "static-access"},
			SecretKey: types.EnvVar{ValueStatic: "static-secret"},
		}
		cfg, err := awssdk.Config(context.New(), conn)
		Expect(err).ToNot(HaveOccurred())

		value, err := cfg.Credentials.Retrieve(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect([]string{cfg.Region, value.AccessKeyID, value.SecretAccessKey}).To(Equal([]string{"eu-west-1", "static-access", "static-secret"}))
	})

	It("presigns the EKS STS URL byte-for-byte as the SDK signer does", func(ctx SpecContext) {
		now := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
		request, err := http.NewRequest(http.MethodGet, "https://sts.amazonaws.com/?Action=GetCallerIdentity&Version=2011-06-15", nil)
		Expect(err).ToNot(HaveOccurred())
		request.Header.Add("x-k8s-aws-id", "prod")
		request.Header.Add("X-Amz-Expires", "86400")
		credentials := aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", SessionToken: "example-token"}
		want, _, err := signerv4.NewSigner().PresignHTTP(gocontext.Background(), credentials, request,
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "sts", "us-east-1", now)
		Expect(err).ToNot(HaveOccurred())

		got, err := cloudaws.PresignGetCallerIdentity(awssigv4.Credentials{AccessKeyID: credentials.AccessKeyID, SecretAccessKey: credentials.SecretAccessKey, SessionToken: credentials.SessionToken}, "prod", now)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal(want))
	})
})
