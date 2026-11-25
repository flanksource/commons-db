package azure_test

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cloud/azure"
)

var _ = Describe("KeyVaultClient", func() {
	var (
		aad *fakeAAD
		got recorded
	)

	BeforeEach(func() { aad = newFakeAAD() })
	AfterEach(func() { aad.Close() })

	vault := func(handler http.HandlerFunc) *azure.KeyVaultClient {
		srv := httptest.NewServer(handler)
		DeferCleanup(srv.Close)
		c, err := azure.NewKeyVaultClient(azure.KeyVaultOptions{VaultURL: srv.URL, Credential: aad.Credential(), Scope: "https://vault.azure.net/.default"})
		Expect(err).ToNot(HaveOccurred())
		return c
	}

	It("encrypts with the documented request shape and returns decoded bytes", func() {
		plaintext := []byte{0xfb, 0xff, 0x00, 'h', 'i'} // base64url differs from std base64 here
		wantValue := base64.RawURLEncoding.EncodeToString(plaintext)
		c := vault(func(w http.ResponseWriter, r *http.Request) {
			got = record(r)
			_, _ = w.Write([]byte(`{"kid":"k","value":"` + base64.RawURLEncoding.EncodeToString([]byte("cipher")) + `"}`))
		})

		out, err := c.Encrypt(ctx(), azure.KeyOperation{Name: "my-key", Version: "v1", Algorithm: azure.AlgRSAOAEP256, Value: plaintext})
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(Equal([]byte("cipher")))
		Expect(got).To(Equal(recorded{
			Method: "POST", Path: "/keys/my-key/v1/encrypt", Query: url.Values{"api-version": {"7.4"}},
			Auth: "Bearer " + fakeAccessToken, Body: map[string]any{"alg": "RSA-OAEP-256", "value": wantValue},
		}))
		Expect(aad.Scopes()).To(Equal([]string{"https://vault.azure.net/.default"}))
	})

	It("decrypts against the decrypt endpoint", func() {
		c := vault(func(w http.ResponseWriter, r *http.Request) {
			got = record(r)
			_, _ = w.Write([]byte(`{"value":"` + base64.RawURLEncoding.EncodeToString([]byte("plain")) + `"}`))
		})
		out, err := c.Decrypt(ctx(), azure.KeyOperation{Name: "my-key", Algorithm: azure.AlgRSAOAEP256, Value: []byte("cipher")})
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(Equal([]byte("plain")))
		Expect(got.Path).To(Equal("/keys/my-key//decrypt"))
	})

	It("returns a typed APIError from the Azure error envelope", func() {
		c := vault(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"KeyNotFound","message":"key missing"}}`))
		})
		_, err := c.Encrypt(ctx(), azure.KeyOperation{Name: "k", Algorithm: azure.AlgRSAOAEP256, Value: []byte("x")})
		var apiErr *azure.APIError
		Expect(err).To(MatchError(ContainSubstring("KeyNotFound")))
		Expect(errorsAs(err, &apiErr)).To(BeTrue())
		Expect(*apiErr).To(Equal(azure.APIError{StatusCode: 404, Code: "KeyNotFound", Message: "key missing"}))
		Expect(azure.IsNotFound(err)).To(BeTrue())
	})

	It("keeps the status code when the error body is not an Azure envelope", func() {
		c := vault(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream down"))
		})
		_, err := c.Decrypt(ctx(), azure.KeyOperation{Name: "k", Algorithm: azure.AlgRSAOAEP256, Value: []byte("x")})
		var apiErr *azure.APIError
		Expect(errorsAs(err, &apiErr)).To(BeTrue())
		Expect(apiErr.StatusCode).To(Equal(502))
		Expect(apiErr.Message).To(ContainSubstring("upstream down"))
		Expect(azure.IsNotFound(err)).To(BeFalse())
	})

	Describe("transport hardening", func() {
		okBody := `{"value":"` + base64.RawURLEncoding.EncodeToString([]byte("plain")) + `"}`
		op := azure.KeyOperation{Name: "k", Algorithm: azure.AlgRSAOAEP256, Value: []byte("x")}

		tlsVault := func(skip bool) *azure.KeyVaultClient {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(okBody))
			}))
			DeferCleanup(srv.Close)
			c, err := azure.NewKeyVaultClient(azure.KeyVaultOptions{VaultURL: srv.URL, Credential: aad.Credential(), Scope: "s", SkipTLSVerify: skip})
			Expect(err).ToNot(HaveOccurred())
			return c
		}

		It("rejects a self-signed certificate by default", func() {
			_, err := tlsVault(false).Decrypt(ctx(), op)
			Expect(err).To(MatchError(ContainSubstring("certificate")))
		})

		It("accepts a self-signed certificate when SkipTLSVerify is set", func() {
			out, err := tlsVault(true).Decrypt(ctx(), op)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(Equal([]byte("plain")))
		})

		DescribeTable("retries transient statuses with the request body intact",
			func(status int) {
				var calls atomic.Int32
				var bodies []string
				var mu sync.Mutex
				c := vault(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					mu.Lock()
					bodies = append(bodies, string(raw))
					mu.Unlock()
					if calls.Add(1) < 3 {
						w.WriteHeader(status)
						return
					}
					_, _ = w.Write([]byte(okBody))
				})
				out, err := c.Decrypt(ctx(), op)
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(Equal([]byte("plain")))
				Expect(calls.Load()).To(BeEquivalentTo(3))
				Expect(bodies).To(HaveLen(3))
				Expect(bodies[2]).To(Equal(bodies[0]))
				Expect(bodies[0]).ToNot(BeEmpty())
			},
			Entry("408", http.StatusRequestTimeout),
			Entry("429", http.StatusTooManyRequests),
			Entry("503", http.StatusServiceUnavailable),
		)

		It("does not retry non-transient statuses", func() {
			var calls atomic.Int32
			c := vault(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusForbidden)
			})
			_, err := c.Decrypt(ctx(), op)
			Expect(err).To(HaveOccurred())
			Expect(calls.Load()).To(BeEquivalentTo(1))
		})
	})

	DescribeTable("derives the token scope from the vault host's cloud suffix",
		func(vaultURL, want string) {
			scope, err := azure.KeyVaultScope(vaultURL)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).To(Equal(want))
		},
		Entry("public", "https://v.vault.azure.net/", "https://vault.azure.net/.default"),
		Entry("us gov", "https://v.vault.usgovcloudapi.net/", "https://vault.usgovcloudapi.net/.default"),
		Entry("china", "https://v.vault.azure.cn", "https://vault.azure.cn/.default"),
	)

	It("rejects a vault URL without a host suffix", func() {
		_, err := azure.KeyVaultScope("https://localhost")
		Expect(err).To(HaveOccurred())
	})
})
