package aws_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
	"github.com/flanksource/commons/http/middlewares/awssigv4"
)

// uriEncodeSegments is the AWS UriEncode a MinIO or AWS server applies to each
// segment of the decoded path to rebuild the canonical URI.
func uriEncodeSegments(decoded string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(decoded); i++ {
		c := decoded[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '.', c == '_', c == '~', c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

// serverAuthorization recomputes the signature the way a server does: from the
// decoded path re-encoded with UriEncode and the headers the client declared signed.
func serverAuthorization(r *http.Request) string {
	GinkgoHelper()
	auth := r.Header.Get("Authorization")
	_, signed, _ := strings.Cut(auth, "SignedHeaders=")
	signed, _, _ = strings.Cut(signed, ",")
	rebuilt, err := http.NewRequest(r.Method, "http://"+r.Host+uriEncodeSegments(r.URL.Path), nil)
	Expect(err).ToNot(HaveOccurred())
	for _, name := range strings.Split(signed, ";") {
		if name != "host" {
			rebuilt.Header.Set(name, r.Header.Get(name))
		}
	}
	signedAt, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	Expect(err).ToNot(HaveOccurred())
	signature, err := awssigv4.Sign(rebuilt, awssigv4.SignOptions{
		Credentials: awsCreds(), Region: testRegion, Service: "s3", Time: signedAt, PayloadHash: r.Header.Get("X-Amz-Content-Sha256"),
	})
	Expect(err).ToNot(HaveOccurred())
	return signature.Authorization
}

var _ = Describe("S3 key encoding", func() {
	DescribeTable("sends and signs the AWS UriEncode form of the key",
		func(ctx SpecContext, key, wantPath string) {
			var sentPath, wantAuth, gotAuth string
			session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
				defer GinkgoRecover()
				sentPath, _, _ = strings.Cut(r.RequestURI, "?")
				wantAuth, gotAuth = serverAuthorization(r), r.Header.Get("Authorization")
				w.Header().Set("Content-Length", "0")
			})
			_, err := session.S3(cloudaws.S3Options{PathStyle: true}).HeadObject(ctx, cloudaws.S3ObjectRef{Bucket: "bkt", Key: key})
			Expect(err).ToNot(HaveOccurred())

			Expect(sentPath).To(Equal("/bkt/" + wantPath))
			Expect(gotAuth).To(Equal(wantAuth))
		},
		Entry("reserved punctuation", "dt=2024-01-01/a:b+c.json", "dt%3D2024-01-01/a%3Ab%2Bc.json"),
		Entry("space and non-ASCII", "space key/ü.txt", "space%20key/%C3%BC.txt"),
		Entry("unreserved only", "a~b_c-d.e", "a~b_c-d.e"),
	)
})

var _ = Describe("throttling retries", func() {
	It("retries an HTTP 400 ThrottlingException and returns the next response", func(ctx SpecContext) {
		var calls atomic.Int32
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"__type":"ThrottlingException","message":"Rate exceeded"}`))
				return
			}
			_, _ = w.Write([]byte(`{"CiphertextBlob":"Y2lwaGVy"}`))
		})
		out, err := session.KMS().Encrypt(ctx, cloudaws.KMSEncryptInput{KeyID: "alias/x", Plaintext: []byte("p")})
		Expect(err).ToNot(HaveOccurred())
		Expect(string(out)).To(Equal("cipher"))
		Expect(calls.Load()).To(Equal(int32(2)))
	})

	It("does not retry a non-throttling 400 and keeps its error body", func(ctx SpecContext) {
		var calls atomic.Int32
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"ValidationException","message":"bad"}`))
		})
		_, err := session.KMS().Encrypt(ctx, cloudaws.KMSEncryptInput{KeyID: "alias/x", Plaintext: []byte("p")})
		var apiErr *cloudaws.APIError
		Expect(errors.As(err, &apiErr)).To(BeTrue())
		Expect(apiErr.Code).To(Equal("ValidationException"))
		Expect(calls.Load()).To(Equal(int32(1)))
	})

	It("gives up after the retry budget on persistent throttling", func(ctx SpecContext) {
		var calls atomic.Int32
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"ThrottlingException","message":"Rate exceeded"}`))
		})
		_, err := session.KMS().Encrypt(ctx, cloudaws.KMSEncryptInput{KeyID: "alias/x", Plaintext: []byte("p")})
		var apiErr *cloudaws.APIError
		Expect(errors.As(err, &apiErr)).To(BeTrue())
		Expect(apiErr.Code).To(Equal("ThrottlingException"))
		Expect(calls.Load()).To(Equal(int32(3)))
	})
})

var _ = Describe("TLS verification", func() {
	newTLSSession := func(skip bool) *cloudaws.Session {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`<GetCallerIdentityResponse><GetCallerIdentityResult><Account>123456789012</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`))
		}))
		DeferCleanup(server.Close)
		session, err := cloudaws.NewSession(cloudaws.SessionOptions{
			Region: testRegion, ServiceEndpoints: map[string]string{cloudaws.ServiceSTS: server.URL}, SkipTLSVerify: skip,
			Credentials: awssigv4.NewStaticCredentialsProvider(awsCreds()),
		})
		Expect(err).ToNot(HaveOccurred())
		return session
	}

	It("rejects a self-signed certificate by default", func(ctx SpecContext) {
		_, err := newTLSSession(false).STS().GetCallerIdentity(ctx)
		Expect(err).To(HaveOccurred())
		Expect(strings.ToLower(err.Error())).To(ContainSubstring("certificate"))
	})

	It("accepts it when SkipTLSVerify is set", func(ctx SpecContext) {
		identity, err := newTLSSession(true).STS().GetCallerIdentity(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(identity.Account).To(Equal("123456789012"))
	})
})
