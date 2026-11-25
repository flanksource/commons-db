package connection

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/flanksource/commons/http/middlewares/awssigv4"
	. "github.com/onsi/gomega"
)

// redirectTransport sends every request to target while recording the URL the
// caller addressed.
type redirectTransport struct {
	target *url.URL
	asked  *url.URL
}

func (r *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.asked = req.URL
	redirected := req.Clone(req.Context())
	redirected.URL.Scheme, redirected.URL.Host = r.target.Scheme, r.target.Host
	return http.DefaultTransport.RoundTrip(redirected)
}

func TestAssumeRoleUsesTheAWSSTSEndpointNotTheConnectionEndpoint(t *testing.T) {
	g := NewWithT(t)
	var form url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(body))
		_, _ = w.Write([]byte(`<AssumeRoleResponse><AssumeRoleResult><Credentials><AccessKeyId>assumed-access</AccessKeyId><SecretAccessKey>assumed-secret</SecretAccessKey>` +
			`<SessionToken>assumed-token</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleResult></AssumeRoleResponse>`))
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	g.Expect(err).ToNot(HaveOccurred())

	conn := &AWSConnection{Region: "eu-west-1", Endpoint: "https://minio.example.com:9000", AssumeRole: "arn:aws:iam::123456789012:role/example"}
	base := awssigv4.NewStaticCredentialsProvider(awssigv4.Credentials{AccessKeyID: "example-access", SecretAccessKey: "example-secret"})
	options := conn.assumeRoleOptions("eu-west-1", base)
	transport := &redirectTransport{target: target}
	options.HTTPClient = &http.Client{Transport: transport}

	creds, err := awssigv4.NewAssumeRoleProvider(options).Retrieve(t.Context())
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(creds.AccessKeyID).To(Equal("assumed-access"))
	g.Expect(form.Get("RoleArn")).To(Equal(conn.AssumeRole))
	g.Expect(transport.asked.Host).To(Equal("sts.eu-west-1.amazonaws.com"))
}
