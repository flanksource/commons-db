package connection

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/flanksource/commons-db/cloud/gcp"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	"github.com/flanksource/commons/hash"
	"github.com/flanksource/commons/utils"
)

// +kubebuilder:object:generate=true
type GCPConnection struct {
	// ConnectionName of the connection. It'll be used to populate the endpoint and credentials.
	ConnectionName string        `yaml:"connection,omitempty" json:"connection,omitempty"`
	Endpoint       string        `yaml:"endpoint" json:"endpoint,omitempty"`
	Credentials    *types.EnvVar `yaml:"credentials" json:"credentials,omitempty"`

	// Skip TLS verify
	SkipTLSVerify bool `yaml:"skipTLSVerify,omitempty" json:"skipTLSVerify,omitempty"`

	Project string `yaml:"project" json:"project,omitempty"`
}

func (t *GCPConnection) ToModel() models.Connection {
	return models.Connection{
		Name:        t.ConnectionName,
		URL:         t.Endpoint,
		Certificate: t.Credentials.String(),
		InsecureTLS: t.SkipTLSVerify,
	}
}

func (t *GCPConnection) FromModel(connection models.Connection) {
	t.ConnectionName = connection.Name
	t.Credentials = &types.EnvVar{ValueStatic: connection.Certificate}
	t.Endpoint = connection.URL
	t.SkipTLSVerify = connection.InsecureTLS
}

func (g *GCPConnection) TokenSource(ctx context.Context, scopes ...string) (oauth2.TokenSource, error) {
	if g.Credentials == nil || g.Credentials.ValueStatic == "" {
		return nil, fmt.Errorf("gcp connection %q has no credentials", g.ConnectionName)
	}

	return gcp.CredentialsTokenSource(ctx, []byte(g.Credentials.ValueStatic), scopes...)
}

// ClientOptions maps the connection onto cloud/gcp client options: its endpoint
// and TLS setting, authenticated with credentials (the resolved service-account
// JSON) scoped to scopes. Empty credentials leave requests unauthenticated,
// which is what emulators expect; callers that want the ambient application
// default credentials ask for them explicitly with gcp.DefaultTokenSource.
func (g *GCPConnection) ClientOptions(ctx context.Context, credentials string, scopes ...string) (gcp.Options, error) {
	opts := gcp.Options{Endpoint: g.Endpoint, SkipTLSVerify: g.SkipTLSVerify}
	if credentials == "" {
		return opts, nil
	}

	tokenSource, err := gcp.CredentialsTokenSource(ctx, []byte(credentials), scopes...)
	if err != nil {
		return gcp.Options{}, err
	}
	opts.TokenSource = tokenSource
	return opts, nil
}

func (g *GCPConnection) Validate() *GCPConnection {
	if g == nil {
		return &GCPConnection{}
	}

	return g
}

func (g *GCPConnection) Token(ctx context.Context, freshToken bool, scopes ...string) (*oauth2.Token, error) {
	cacheKey := tokenCacheKey("gcp", hash.Sha256Hex(g.Credentials.ValueStatic), strings.Join(scopes, ","))
	if !freshToken {
		if found, ok := tokenCache.Get(cacheKey); ok {
			return found.(*oauth2.Token), nil
		}
	}

	tokenSource, err := g.TokenSource(ctx, scopes...)
	if err != nil {
		return nil, err
	}

	token, err := tokenSource.Token()
	if err != nil {
		return nil, err
	}

	tokenCache.Set(cacheKey, token, time.Until(token.Expiry)-tokenSafetyMargin)
	return token, nil
}

// HydrateConnection attempts to find the connection by name
// and populate the endpoint and credentials.
func (g *GCPConnection) HydrateConnection(ctx ConnectionContext) error {
	connection, err := ctx.HydrateConnectionByURL(g.ConnectionName)
	if err != nil {
		return err
	}

	if connection != nil {
		g.Credentials = &types.EnvVar{ValueStatic: connection.Certificate}
		g.Endpoint = connection.URL

		// Every GCP client is scoped to a project, so a connection that stores
		// one has to hand it over — otherwise the caller is left to supply the
		// project a second time alongside a connection that already knows it.
		if g.Project == "" {
			g.Project = connection.Properties["project"]
		}
	}

	if g.Credentials != nil {
		if cred, err := ctx.GetEnvValueFromCache(*g.Credentials, ctx.GetNamespace()); err != nil {
			return fmt.Errorf("could not get GCP credentials from env var: %w", err)
		} else {
			g.Credentials.ValueStatic = cred
		}
	}

	return nil
}

func (t *GCPConnection) GetCertificate() types.EnvVar {
	return utils.Deref(t.Credentials)
}

func (t *GCPConnection) GetURL() types.EnvVar {
	return types.EnvVar{ValueStatic: t.Endpoint}
}
