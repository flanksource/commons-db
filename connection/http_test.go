package connection

import (
	"testing"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

func TestHTTPConnectionAuthenticationModes(t *testing.T) {
	tests := []struct {
		name       string
		properties types.JSONStringMap
		assert     func(*testing.T, HTTPConnection)
	}{
		{
			name:       "none ignores stale credentials",
			properties: types.JSONStringMap{"authType": "none", "clientID": "stale-client"},
			assert: func(t *testing.T, got HTTPConnection) {
				if !got.HTTPBasicAuth.IsEmpty() || !got.OAuth.IsEmpty() || !got.TLS.Cert.IsEmpty() {
					t.Fatalf("none auth loaded credentials: %+v", got)
				}
			},
		},
		{
			name: "basic",
			properties: types.JSONStringMap{
				"authType": "basic", "username": "api-user", "password": "api-password",
				"clientID": "stale-client", "clientSecret": "stale-secret", "tokenURL": "https://stale/token",
			},
			assert: func(t *testing.T, got HTTPConnection) {
				if got.GetUsername() != "api-user" || got.GetPassword() != "api-password" {
					t.Fatalf("basic credentials = %q/%q", got.GetUsername(), got.GetPassword())
				}
				if !got.OAuth.IsEmpty() {
					t.Fatalf("basic auth loaded stale OAuth credentials: %+v", got.OAuth)
				}
			},
		},
		{
			name: "oauth",
			properties: types.JSONStringMap{
				"authType": "oauth", "clientID": "client", "clientSecret": "secret",
				"tokenURL": "https://issuer/token", "scopes": "read,write",
			},
			assert: func(t *testing.T, got HTTPConnection) {
				if !got.HTTPBasicAuth.IsEmpty() {
					t.Fatalf("oauth loaded basic credentials: %+v", got.HTTPBasicAuth)
				}
				if got.OAuth.ClientID.ValueStatic != "client" || got.OAuth.ClientSecret.ValueStatic != "secret" || got.OAuth.TokenURL != "https://issuer/token" {
					t.Fatalf("OAuth credentials = %+v", got.OAuth)
				}
				if len(got.OAuth.Scopes) != 2 || got.OAuth.Scopes[0] != "read" || got.OAuth.Scopes[1] != "write" {
					t.Fatalf("OAuth scopes = %#v", got.OAuth.Scopes)
				}
			},
		},
		{
			name: "mtls",
			properties: types.JSONStringMap{
				"authType": "mtls", "ca": "ca-pem", "cert": "cert-pem", "key": "key-pem",
			},
			assert: func(t *testing.T, got HTTPConnection) {
				if got.TLS.CA.ValueStatic != "ca-pem" || got.TLS.Cert.ValueStatic != "cert-pem" || got.TLS.Key.ValueStatic != "key-pem" {
					t.Fatalf("mTLS credentials = %+v", got.TLS)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got HTTPConnection
			err := got.FromModel(models.Connection{
				Type: models.ConnectionTypeHTTP, URL: "https://example.com",
				Username: "legacy-user", Password: "legacy-password", Properties: tt.properties,
			})
			if err != nil {
				t.Fatal(err)
			}
			tt.assert(t, got)
		})
	}
}

func TestTLSConfigRequiresClientCertificateAndKeyTogether(t *testing.T) {
	_, err := (TLSConfig{Cert: types.EnvVar{ValueStatic: "cert-only"}}).transportConfig()
	if err == nil {
		t.Fatal("expected incomplete mTLS credentials to fail")
	}
}
