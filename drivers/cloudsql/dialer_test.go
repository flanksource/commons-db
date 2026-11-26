package cloudsql

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/oauth2"

	"github.com/flanksource/commons-db/cloud/gcp"
)

func TestCloudSQL(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "drivers/cloudsql")
}

const (
	testInstance   = "proj:us-east1:inst"
	testServerName = "inst.proj.db.test"
	testLoginToken = "login-token"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(commonName string) testCA {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	Expect(err).NotTo(HaveOccurred())
	cert, err := x509.ParseCertificate(der)
	Expect(err).NotTo(HaveOccurred())
	return testCA{cert: cert, key: key}
}

func (ca testCA) pem() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw}))
}

func (ca testCA) pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	return pool
}

func (ca testCA) issue(template *x509.Certificate, public any) []byte {
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, public, ca.key)
	Expect(err).NotTo(HaveOccurred())
	return der
}

func (ca testCA) serverCertificate() tls.Certificate {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	der := ca.issue(&x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "proj:inst"},
		DNSNames:     []string{testServerName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, &key.PublicKey)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

type staticLoginToken struct{}

func (staticLoginToken) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: testLoginToken, Expiry: time.Now().Add(time.Hour)}, nil
}

// fakeCloudSQL is a sqladmin API plus a TLS proxy that requires a client
// certificate issued by the fake ephemeral-cert endpoint.
type fakeCloudSQL struct {
	admin        *httptest.Server
	listener     net.Listener
	serverCA     testCA
	serverCAMode string
	dnsName      string
	clientCA     testCA
	certValidity time.Duration

	mu          sync.Mutex
	certLogins  []string
	issuedKeys  []*rsa.PublicKey
	dialedAddrs []string
	peerCerts   chan []*x509.Certificate
}

func newFakeCloudSQL(serverCA testCA, certValidity time.Duration) *fakeCloudSQL {
	f := &fakeCloudSQL{serverCA: serverCA, clientCA: newTestCA("client-ca"), certValidity: certValidity, peerCerts: make(chan []*x509.Certificate, 8)}
	f.admin = httptest.NewServer(http.HandlerFunc(f.serveAdmin))
	f.dnsName = testServerName
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{serverCA.serverCertificate()},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    f.clientCA.pool(),
		MinVersion:   tls.VersionTLS13,
	})
	Expect(err).NotTo(HaveOccurred())
	f.listener = listener
	go f.serveProxy()
	return f
}

func (f *fakeCloudSQL) Close() {
	f.admin.Close()
	_ = f.listener.Close()
}

func (f *fakeCloudSQL) serveProxy() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			tlsConn := conn.(*tls.Conn)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			f.peerCerts <- tlsConn.ConnectionState().PeerCertificates
			_, _ = io.WriteString(tlsConn, "hello")
		}()
	}
}

func (f *fakeCloudSQL) serveAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"region": "us-east1", "backendType": "SECOND_GEN", "databaseVersion": "POSTGRES_15", "dnsName": f.dnsName, "serverCaMode": f.serverCAMode,
			"serverCaCert": map[string]string{"cert": f.serverCA.pem()},
			"ipAddresses": []map[string]string{
				{"type": "PRIMARY", "ipAddress": "203.0.113.7"},
				{"type": "PRIVATE", "ipAddress": "10.0.0.5"},
			},
		})
		return
	}
	var req struct {
		PublicKey   string `json:"public_key"`
		AccessToken string `json:"access_token"`
	}
	Expect(json.NewDecoder(r.Body).Decode(&req)).To(Succeed())
	block, _ := pem.Decode([]byte(req.PublicKey))
	Expect(block).NotTo(BeNil())
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	Expect(err).NotTo(HaveOccurred())
	public := parsed.(*rsa.PublicKey)
	f.mu.Lock()
	f.certLogins = append(f.certLogins, req.AccessToken)
	f.issuedKeys = append(f.issuedKeys, public)
	f.mu.Unlock()
	expiry := time.Now().Add(f.certValidity)
	der := f.clientCA.issue(&x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "iam-user"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     expiry,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, public)
	_ = json.NewEncoder(w).Encode(map[string]any{"ephemeralCert": map[string]any{
		"cert":           string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		"expirationTime": expiry.UTC().Format(time.RFC3339),
	}})
}

func (f *fakeCloudSQL) dialer() *dialer {
	admin, err := gcp.NewSQLAdminClient(gcp.Options{Endpoint: f.admin.URL})
	Expect(err).NotTo(HaveOccurred())
	return newDialer(dialerConfig{
		Admin:       admin,
		LoginTokens: staticLoginToken{},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			f.mu.Lock()
			f.dialedAddrs = append(f.dialedAddrs, addr)
			f.mu.Unlock()
			return (&net.Dialer{}).DialContext(ctx, network, f.listener.Addr().String())
		},
	})
}

func readGreeting(conn net.Conn) string {
	defer conn.Close()
	data, err := io.ReadAll(conn)
	Expect(err).NotTo(HaveOccurred())
	return string(data)
}

var _ = Describe("Cloud SQL dialer", func() {
	var sql *fakeCloudSQL
	AfterEach(func() { sql.Close() })

	It("connects over mutual TLS to the public proxy port with an IAM-bearing ephemeral cert", func() {
		sql = newFakeCloudSQL(newTestCA("server-ca"), time.Hour)
		cloudDialer := sql.dialer()

		conn, err := cloudDialer.Dial(context.Background(), dialOptions{InstanceConnectionName: testInstance})

		Expect(err).NotTo(HaveOccurred())
		Expect(readGreeting(conn)).To(Equal("hello"))
		peer := <-sql.peerCerts
		Expect(peer).NotTo(BeEmpty())
		Expect(sql.dialedAddrs).To(Equal([]string{"203.0.113.7:3307"}))
		Expect(sql.certLogins).To(Equal([]string{testLoginToken}))
		Expect(sql.issuedKeys[0].N.BitLen()).To(Equal(2048))
	})

	It("dials the private address when asked", func() {
		sql = newFakeCloudSQL(newTestCA("server-ca"), time.Hour)

		conn, err := sql.dialer().Dial(context.Background(), dialOptions{InstanceConnectionName: testInstance, PrivateIP: true})

		Expect(err).NotTo(HaveOccurred())
		Expect(readGreeting(conn)).To(Equal("hello"))
		Expect(sql.dialedAddrs).To(Equal([]string{"10.0.0.5:3307"}))
	})

	It("reuses the ephemeral cert across dials until it nears expiry, then requests a new one", func() {
		sql = newFakeCloudSQL(newTestCA("server-ca"), time.Hour)
		cloudDialer := sql.dialer()
		for range 2 {
			conn, err := cloudDialer.Dial(context.Background(), dialOptions{InstanceConnectionName: testInstance})
			Expect(err).NotTo(HaveOccurred())
			Expect(readGreeting(conn)).To(Equal("hello"))
		}
		Expect(sql.certLogins).To(HaveLen(1))

		sql.certValidity = 2 * time.Minute // inside the 4 minute refresh buffer
		cloudDialer.cache[mustConnName(testInstance)].invalidate()
		for range 2 {
			conn, err := cloudDialer.Dial(context.Background(), dialOptions{InstanceConnectionName: testInstance})
			Expect(err).NotTo(HaveOccurred())
			Expect(readGreeting(conn)).To(Equal("hello"))
		}

		Expect(sql.certLogins).To(HaveLen(3), "the short-lived cert is refreshed on every dial")
		Expect(sql.issuedKeys[2].Equal(sql.issuedKeys[0])).To(BeTrue(), "the RSA key is generated once per dialer")
	})

	It("refuses a server certificate that the instance's server CA did not sign", func() {
		sql = newFakeCloudSQL(newTestCA("server-ca"), time.Hour)
		sql.serverCA = newTestCA("someone-else") // metadata now advertises a different CA

		_, err := sql.dialer().Dial(context.Background(), dialOptions{InstanceConnectionName: testInstance})

		Expect(err).To(MatchError(ContainSubstring("verifying server certificate")))
	})

	It("rejects a shared-CA certificate for another DNS name even with a matching common name", func() {
		sql = newFakeCloudSQL(newTestCA("shared-ca"), time.Hour)
		sql.serverCAMode = "GOOGLE_MANAGED_CAS_CA"
		sql.dnsName = "other.proj.db.test"
		_, err := sql.dialer().Dial(context.Background(), dialOptions{InstanceConnectionName: testInstance})
		Expect(err).To(MatchError(ContainSubstring(sql.dnsName)))
	})

	It("rejects a malformed instance connection name before any request", func() {
		sql = newFakeCloudSQL(newTestCA("server-ca"), time.Hour)

		_, err := sql.dialer().Dial(context.Background(), dialOptions{InstanceConnectionName: "not-a-name"})

		Expect(err).To(MatchError(ContainSubstring("expected PROJECT:REGION:INSTANCE")))
		Expect(sql.certLogins).To(BeEmpty())
	})

	It("rejects a region that differs from the instance's", func() {
		sql = newFakeCloudSQL(newTestCA("server-ca"), time.Hour)

		_, err := sql.dialer().Dial(context.Background(), dialOptions{InstanceConnectionName: "proj:europe-west1:inst"})

		Expect(err).To(MatchError(ContainSubstring("region was mismatched")))
	})
})

func mustConnName(raw string) connName {
	name, err := parseConnName(raw)
	Expect(err).NotTo(HaveOccurred())
	return name
}
