package connection_test

import (
	gocontext "context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
	"github.com/flanksource/commons/http/middlewares/awssigv4"
)

var _ = Describe("AWSConnection credentials", func() {
	clearAWSEnv := func() {
		for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_REGION", "AWS_DEFAULT_REGION"} {
			GinkgoT().Setenv(name, "")
		}
		GinkgoT().Setenv("AWS_EC2_METADATA_DISABLED", "true")
	}
	static := func() connection.AWSConnection {
		return connection.AWSConnection{
			Region:       "eu-west-1",
			AccessKey:    types.EnvVar{ValueStatic: "example-access"},
			SecretKey:    types.EnvVar{ValueStatic: "example-secret"},
			SessionToken: types.EnvVar{ValueStatic: "example-session"},
		}
	}
	BeforeEach(clearAWSEnv)

	It("uses the connection's static access key, secret and session token", func(ctx SpecContext) {
		conn := static()
		session, err := conn.Client(context.New())
		Expect(err).ToNot(HaveOccurred())

		creds, err := session.Credentials().Retrieve(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(creds).To(Equal(awssigv4.Credentials{AccessKeyID: "example-access", SecretAccessKey: "example-secret", SessionToken: "example-session"}))
		Expect(session.Region()).To(Equal("eu-west-1"))
	})

	It("falls back to the environment when the connection has no keys", func(ctx SpecContext) {
		GinkgoT().Setenv("AWS_ACCESS_KEY_ID", "env-access")
		GinkgoT().Setenv("AWS_SECRET_ACCESS_KEY", "env-secret")
		session, err := (&connection.AWSConnection{Region: "us-east-1"}).Client(context.New())
		Expect(err).ToNot(HaveOccurred())

		creds, err := session.Credentials().Retrieve(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(creds.AccessKeyID).To(Equal("env-access"))
	})

	It("takes the region from AWS_REGION when the connection has none", func() {
		GinkgoT().Setenv("AWS_REGION", "ap-south-1")
		conn := static()
		conn.Region = ""
		session, err := conn.Client(context.New())
		Expect(err).ToNot(HaveOccurred())
		Expect(session.Region()).To(Equal("ap-south-1"))
	})

	It("requires a region", func() {
		conn := static()
		conn.Region = ""
		_, err := conn.Client(context.New())
		Expect(err).To(MatchError(ContainSubstring("region")))
	})

	It("names the sources it tried and points at awssdk.Install when nothing resolves", func(ctx SpecContext) {
		session, err := (&connection.AWSConnection{Region: "us-east-1"}).Client(context.New())
		Expect(err).ToNot(HaveOccurred())

		_, err = session.Credentials().Retrieve(ctx)
		Expect(err).To(HaveOccurred())
		for _, want := range []string{"connection", "environment", "web identity", "IMDSv2", "connection/awssdk", "awssdk.Install()"} {
			Expect(err.Error()).To(ContainSubstring(want))
		}
	})

	It("uses the registered SDK hook instead of the built-in chain when the connection has no keys", func(ctx SpecContext) {
		connection.RegisterAWSCredentialsHook(func(_ gocontext.Context, _ *connection.AWSConnection) (connection.AWSHookCredentials, error) {
			return connection.AWSHookCredentials{
				Region:   "hook-region",
				Provider: awssigv4.NewStaticCredentialsProvider(awssigv4.Credentials{AccessKeyID: "hook-access", SecretAccessKey: "hook-secret"}),
			}, nil
		})
		DeferCleanup(func() { connection.RegisterAWSCredentialsHook(nil) })

		session, err := (&connection.AWSConnection{}).Client(context.New())
		Expect(err).ToNot(HaveOccurred())
		creds, err := session.Credentials().Retrieve(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(creds.AccessKeyID).To(Equal("hook-access"))
		Expect(session.Region()).To(Equal("hook-region"))
	})
})
