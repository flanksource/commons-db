package connection

import (
	"testing"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

// The connection form stores a Loki endpoint's credentials the same way it does
// for every other HTTP-family backend — under Properties, keyed by authType —
// so Populate has to read that contract rather than the legacy columns.
func TestLokiPopulateReadsFormAuthentication(t *testing.T) {
	tests := []struct {
		name       string
		properties types.JSONStringMap
		assert     func(*testing.T, Loki)
	}{
		{
			name:       "basic",
			properties: types.JSONStringMap{"authType": "basic", "username": "api-user", "password": "api-password"},
			assert: func(t *testing.T, got Loki) {
				if got.GetUsername() != "api-user" || got.GetPassword() != "api-password" {
					t.Fatalf("basic credentials = %q/%q", got.GetUsername(), got.GetPassword())
				}
			},
		},
		{
			name: "oauth",
			properties: types.JSONStringMap{
				"authType": "oauth", "clientID": "client", "clientSecret": "secret",
				"tokenURL": "https://issuer/token",
			},
			assert: func(t *testing.T, got Loki) {
				if got.OAuth.ClientID.ValueStatic != "client" || got.OAuth.TokenURL != "https://issuer/token" {
					t.Fatalf("OAuth credentials = %+v", got.OAuth)
				}
			},
		},
		{
			name:       "mtls",
			properties: types.JSONStringMap{"authType": "mtls", "cert": "cert-pem", "key": "key-pem"},
			assert: func(t *testing.T, got Loki) {
				if got.TLS.Cert.ValueStatic != "cert-pem" || got.TLS.Key.ValueStatic != "key-pem" {
					t.Fatalf("mTLS credentials = %+v", got.TLS)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := Loki{HTTPConnection: HTTPConnection{ConnectionName: "connection://loki"}}
			ctx := newStubContext(&models.Connection{
				Name: "loki", Type: models.ConnectionTypeLoki,
				URL: "https://loki.example.com", InsecureTLS: true,
				Properties: tt.properties,
			})
			if err := conn.Populate(ctx); err != nil {
				t.Fatal(err)
			}
			if conn.URL != "https://loki.example.com" {
				t.Fatalf("url = %q", conn.URL)
			}
			if !conn.TLS.InsecureSkipVerify {
				t.Fatal("insecure_tls on the stored connection was dropped")
			}
			tt.assert(t, conn)
		})
	}
}
