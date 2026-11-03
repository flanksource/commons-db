package git

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/git/readcache"
	"github.com/flanksource/commons-db/models"
	commonshttp "github.com/flanksource/commons/http"
)

// Client is a git connection: its provider API and the credentials for its
// repositories.
type Client struct {
	Type     string
	Provider Provider
	Auth     Auth

	scope             ConnectionScope
	readCache         *readcache.Cache
	securityNamespace string
}

// ClientOptions shares transport connections while keeping middleware per client.
type ClientOptions struct {
	TransportPool     *commonshttp.TransportPool
	SecurityNamespace string
	ReadCache         *readcache.Cache
}

// FromConnection builds a Client from a hydrated connection (URL, username,
// password and certificate already resolved). Type git uses
// Username/Password, with Certificate as the SSH private key, and its URL is
// the repository. github, gitlab and azure_devops carry a PAT in Password and
// the API URL in URL; the namespace comes from Properties organization,
// group or project respectively.
func FromConnection(ctx dbcontext.Context, conn models.Connection, options ...ClientOptions) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(options) > 1 {
		return nil, fmt.Errorf("git client accepts one options value")
	}
	clientOptions := ClientOptions{}
	if len(options) == 1 {
		clientOptions = options[0]
	}
	encoded, err := json.Marshal(conn)
	if err != nil {
		return nil, fmt.Errorf("encode git connection identity: %w", err)
	}
	clientOptions.SecurityNamespace += fmt.Sprintf("\x00%x", sha256.Sum256(encoded))
	scope, err := ScopeOf(conn)
	if err != nil {
		return nil, err
	}
	tls := TLS{InsecureSkipVerify: conn.InsecureTLS}
	if conn.Type == models.ConnectionTypeGit {
		client, err := gitClient(conn, scope, tls)
		if err == nil {
			client.readCache, client.securityNamespace = clientOptions.ReadCache, clientOptions.SecurityNamespace
		}
		return client, err
	}
	return providerClient(conn, scope, tls, clientOptions)
}

func gitClient(conn models.Connection, scope ConnectionScope, tls TLS) (*Client, error) {
	auth := Auth{Username: conn.Username, Password: conn.Password, PrivateKey: conn.Certificate, TLS: tls}
	repo, err := NewRepository(RepositoryOptions{URL: conn.URL, Auth: auth, ProviderType: models.ConnectionTypeGit})
	if err != nil {
		return nil, fmt.Errorf("git connection %q: %w", conn.Name, err)
	}
	return &Client{Type: conn.Type, Provider: &genericProvider{repo: repo}, Auth: auth, scope: scope}, nil
}

func providerClient(conn models.Connection, scope ConnectionScope, tls TLS, options ClientOptions) (*Client, error) {
	if strings.TrimSpace(conn.Password) == "" {
		return nil, fmt.Errorf("%s access token is required", conn.Type)
	}
	auth := Auth{Username: conn.Username, Token: conn.Password, TLS: tls}
	authorization := "Bearer " + conn.Password
	if conn.Type == models.ConnectionTypeAzureDevops {
		authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+conn.Password))
	}
	api, err := newRESTClient(restClientOptions{Provider: conn.Type, BaseURL: scope.API.String(), TLS: tls, Authorization: authorization, Client: options})
	if err != nil {
		return nil, err
	}
	client := &Client{Type: conn.Type, Auth: auth, scope: scope}
	client.readCache, client.securityNamespace = options.ReadCache, options.SecurityNamespace
	switch conn.Type {
	case models.ConnectionTypeGithub:
		client.Provider = &githubProvider{api: api, namespace: scope.Namespace}
	case models.ConnectionTypeGitlab:
		client.Provider = &gitlabProvider{api: api, namespace: scope.Namespace}
	default:
		client.Provider = &azureProvider{api: api, project: scope.Namespace}
	}
	return client, nil
}

// Scope is what the connection may open (ScopeOf).
func (c *Client) Scope() ConnectionScope { return c.scope }

func (c *Client) CacheKey() string {
	identity, err := json.Marshal(struct {
		Type                         string
		Auth                         Auth
		URL                          *url.URL
		Namespace, SecurityNamespace string
	}{c.Type, c.Auth, c.scope.API, c.scope.Namespace, c.securityNamespace})
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(identity))
}

// Repository opens rawURL with the connection's credentials and provider,
// refusing a repository outside the connection's host and organization.
func (c *Client) Repository(rawURL string) (*Repository, error) {
	repo, err := NewRepository(RepositoryOptions{URL: rawURL, Auth: c.Auth, Provider: c.Provider})
	if err != nil {
		return nil, err
	}
	if err := c.scope.Owns(repo.URL); err != nil {
		return nil, fmt.Errorf("repository %s: %w", repo.URL.Redacted(), err)
	}
	repo.ReadCache, repo.SecurityNamespace = c.readCache, c.securityNamespace
	return repo, nil
}
