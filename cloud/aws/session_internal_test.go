package aws

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons/http/middlewares/awssigv4"
)

func testSession(options SessionOptions) (*Session, error) {
	options.Region = "eu-west-2"
	options.Credentials = awssigv4.NewStaticCredentialsProvider(awssigv4.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"})
	return NewSession(options)
}

var _ = Describe("endpoint override", func() {
	DescribeTable("normalizeEndpoint",
		func(raw, want, wantErr string) {
			got, err := normalizeEndpoint(raw)
			if wantErr != "" {
				Expect(err).To(MatchError(ContainSubstring(wantErr)))
				return
			}
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("empty means no override", "", "", ""),
		Entry("host and port", "http://localhost:9000", "http://localhost:9000", ""),
		Entry("trailing slash", "https://minio.example.com/", "https://minio.example.com", ""),
		Entry("path prefix is rejected", "https://example.com/s3", "", `"https://example.com/s3"`),
		Entry("scheme-less host is rejected", "localhost:9000", "", `"localhost:9000"`),
		Entry("unsupported scheme", "ftp://example.com", "", `"ftp://example.com"`),
		Entry("query is rejected", "https://example.com?x=1", "", `"https://example.com?x=1"`),
	)

	It("applies Endpoint to S3, SQS, CloudWatch and Logs only, as the SDK clients did", func() {
		session, err := testSession(SessionOptions{Endpoint: "http://emulator:4566"})
		Expect(err).ToNot(HaveOccurred())

		Expect(session.S3(S3Options{PathStyle: true}).svc.baseURL).To(Equal("http://emulator:4566"))
		Expect(session.SQS().svc.baseURL).To(Equal("http://emulator:4566"))
		Expect(session.CloudWatch().svc.baseURL).To(Equal("http://emulator:4566"))
		Expect(session.Logs().svc.baseURL).To(Equal("http://emulator:4566"))
		Expect(session.STS().svc.baseURL).To(Equal("https://sts.eu-west-2.amazonaws.com"))
		Expect(session.KMS().svc.baseURL).To(Equal("https://kms.eu-west-2.amazonaws.com"))
		Expect(session.EKS().svc.baseURL).To(Equal("https://eks.eu-west-2.amazonaws.com"))
	})

	It("applies an explicit ServiceEndpoints entry to just that service", func() {
		session, err := testSession(SessionOptions{ServiceEndpoints: map[string]string{ServiceSTS: "http://emulator:4566"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(session.STS().svc.baseURL).To(Equal("http://emulator:4566"))
		Expect(session.KMS().svc.baseURL).To(Equal("https://kms.eu-west-2.amazonaws.com"))
	})

	It("rejects an unknown ServiceEndpoints key", func() {
		_, err := testSession(SessionOptions{ServiceEndpoints: map[string]string{"nope": "http://emulator:4566"}})
		Expect(err).To(MatchError(ContainSubstring(`"nope"`)))
	})

	DescribeTable("S3 addressing on a custom endpoint honours PathStyle",
		func(pathStyle bool, wantBase, wantPath string) {
			session, err := testSession(SessionOptions{Endpoint: "http://emulator:4566"})
			Expect(err).ToNot(HaveOccurred())
			base, path := session.S3(S3Options{PathStyle: pathStyle}).target("bkt", "dir/k")
			Expect(base).To(Equal(wantBase))
			Expect(path).To(Equal(wantPath))
		},
		Entry("path style", true, "http://emulator:4566", "/bkt/dir/k"),
		Entry("virtual host puts the bucket in the endpoint subdomain", false, "http://bkt.emulator:4566", "/dir/k"),
	)

	DescribeTable("escapePath uses AWS UriEncode per segment",
		func(key, want string) { Expect(escapePath("bkt", key)).To(Equal("/bkt/" + want)) },
		Entry("reserved punctuation", "dt=2024-01-01/a:b+c.json", "dt%3D2024-01-01/a%3Ab%2Bc.json"),
		Entry("space and non-ASCII", "space key/ü.txt", "space%20key/%C3%BC.txt"),
		Entry("unreserved kept", "a~b_c-d.e", "a~b_c-d.e"),
	)
})
