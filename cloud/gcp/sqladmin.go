package gcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	sqlAdminDefaultRoot = "https://sqladmin.googleapis.com"
	sqlAdminAPIPath     = "/sql/v1beta4"
)

// SQLAdminClient talks to the Cloud SQL Admin REST API (v1beta4), which hands
// out the connection metadata and ephemeral client certificates the Cloud SQL
// dialer needs.
type SQLAdminClient struct{ *api }

// NewSQLAdminClient builds a Cloud SQL Admin client.
func NewSQLAdminClient(opts Options) (*SQLAdminClient, error) {
	client, err := newAPI(opts, sqlAdminDefaultRoot, sqlAdminAPIPath)
	if err != nil {
		return nil, err
	}
	return &SQLAdminClient{api: client}, nil
}

// SQLInstanceRef names a Cloud SQL instance.
type SQLInstanceRef struct {
	Project  string
	Instance string
}

// ConnectSettings is the subset of connectSettings the dialer reads.
type ConnectSettings struct {
	Region          string `json:"region"`
	BackendType     string `json:"backendType"`
	DatabaseVersion string `json:"databaseVersion"`
	ServerCaMode    string `json:"serverCaMode"`
	ServerCaCert    *struct {
		// Cert is the PEM-encoded server CA chain.
		Cert string `json:"cert"`
	} `json:"serverCaCert"`
	IPAddresses []struct {
		// Type is PRIMARY (public), PRIVATE or OUTGOING.
		Type      string `json:"type"`
		IPAddress string `json:"ipAddress"`
	} `json:"ipAddresses"`
	PscEnabled bool   `json:"pscEnabled"`
	DNSName    string `json:"dnsName"`
	DNSNames   []struct {
		Name           string `json:"name"`
		ConnectionType string `json:"connectionType"`
		DNSScope       string `json:"dnsScope"`
	} `json:"dnsNames"`
}

func (s *SQLAdminClient) instanceURL(ref SQLInstanceRef) (string, error) {
	if ref.Project == "" || ref.Instance == "" {
		return "", fmt.Errorf("gcp: cloud sql instance needs project and name, got %+v", ref)
	}
	return fmt.Sprintf("%s/projects/%s/instances/%s", s.base, url.PathEscape(ref.Project), url.PathEscape(ref.Instance)), nil
}

// GetConnectSettings reads an instance's connection metadata.
func (s *SQLAdminClient) GetConnectSettings(ctx context.Context, ref SQLInstanceRef) (*ConnectSettings, error) {
	base, err := s.instanceURL(ref)
	if err != nil {
		return nil, err
	}
	var settings ConnectSettings
	if err := s.do(ctx, call{Method: http.MethodGet, URL: base + "/connectSettings", Out: &settings}); err != nil {
		return nil, err
	}
	return &settings, nil
}

// EphemeralCertRequest asks for a short-lived client certificate.
type EphemeralCertRequest struct {
	SQLInstanceRef
	// PublicKeyPEM is the PEM-encoded public key the certificate is issued for.
	PublicKeyPEM string
	// AccessToken, when set, is embedded in the certificate to authenticate an
	// IAM database user; it must carry the sqlservice.login scope.
	AccessToken string
}

// EphemeralCert is an issued client certificate.
type EphemeralCert struct {
	// CertPEM is the PEM-encoded client certificate.
	CertPEM    string
	Expiration time.Time
}

// GenerateEphemeralCert requests a client certificate for the supplied public key.
func (s *SQLAdminClient) GenerateEphemeralCert(ctx context.Context, req EphemeralCertRequest) (*EphemeralCert, error) {
	base, err := s.instanceURL(req.SQLInstanceRef)
	if err != nil {
		return nil, err
	}
	body := struct {
		PublicKey   string `json:"public_key"`
		AccessToken string `json:"access_token,omitempty"`
	}{PublicKey: req.PublicKeyPEM, AccessToken: req.AccessToken}
	var out struct {
		EphemeralCert struct {
			Cert           string    `json:"cert"`
			ExpirationTime time.Time `json:"expirationTime"`
		} `json:"ephemeralCert"`
	}
	if err := s.do(ctx, call{Method: http.MethodPost, URL: base + ":generateEphemeralCert", Body: body, Out: &out}); err != nil {
		return nil, err
	}
	return &EphemeralCert{CertPEM: out.EphemeralCert.Cert, Expiration: out.EphemeralCert.ExpirationTime}, nil
}
