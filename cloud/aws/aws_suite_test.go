package aws_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
	"github.com/flanksource/commons/http/middlewares/awssigv4"
)

func TestAWS(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "cloud/aws")
}

const (
	testAccessKey = "AKIDEXAMPLE"
	testSecretKey = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	testRegion    = "eu-west-2"
)

// recorded is one request captured by a fake AWS endpoint.
type recorded struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   string
}

// fakeAWS serves handler on an httptest server and returns a session pointed at it.
func fakeAWS(handler func(w http.ResponseWriter, r *http.Request)) (*cloudaws.Session, *httptest.Server) {
	server := httptest.NewServer(http.HandlerFunc(handler))
	DeferCleanup(server.Close)
	session, err := cloudaws.NewSession(cloudaws.SessionOptions{
		Region:   testRegion,
		Endpoint: server.URL,
		ServiceEndpoints: map[string]string{
			cloudaws.ServiceSTS: server.URL, cloudaws.ServiceKMS: server.URL, cloudaws.ServiceEKS: server.URL,
		},
		Credentials: awssigv4.NewStaticCredentialsProvider(awssigv4.Credentials{
			AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey, SessionToken: "example-token",
		}),
	})
	Expect(err).ToNot(HaveOccurred())
	return session, server
}

func awsCreds() awssigv4.Credentials {
	return awssigv4.Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey, SessionToken: "example-token"}
}

func base64URLDecode(s string) (string, error) {
	b, err := base64.URLEncoding.DecodeString(s)
	return string(b), err
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// expectSigned asserts the request carries a SigV4 header scoped to service in the test region.
func expectSigned(r *http.Request, service string) {
	GinkgoHelper()
	auth := r.Header.Get("Authorization")
	Expect(auth).To(HavePrefix("AWS4-HMAC-SHA256 Credential=" + testAccessKey + "/"))
	Expect(auth).To(ContainSubstring("/" + testRegion + "/" + service + "/aws4_request"))
	Expect(r.Header.Get("X-Amz-Security-Token")).To(Equal("example-token"))
}
