package git

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	commonshttp "github.com/flanksource/commons/http"
)

// Client is a git connection: its provider API and the credentials for its
// repositories.
type Client struct {
	Type     string
	Provider Provider
	Auth     Auth

	connectionURL *url.URL
	namespace     string
}

// ClientOptions shares transport connections while keeping middleware per client.
type ClientOptions struct {
	TransportPool     *commonshttp.TransportPool
	SecurityNamespace string
}

// FromConnection builds a Client from a hydrated connection (URL, username,
// password and certificate already resolved). Type git uses
// Username/Password, with Certificate as the SSH private key, and its URL is
// the repository. github, gitlab and azure_devops carry a PAT in Password and
// the API URL in URL; the namespace comes from Properties organization,
// group or project respectively.
func FromConnection(ctx dbcontext.Context, conn models.Connection, options ...ClientOptions) (*Client, error) {
	if len(options) > 1 {
		return nil, fmt.Errorf("git client accepts one options value")
	}
	clientOptions := ClientOptions{}
	if len(options) == 1 {
		clientOptions = options[0]
		encoded, err := json.Marshal(conn)
		if err != nil {
			return nil, fmt.Errorf("encode git connection identity: %w", err)
		}
		clientOptions.SecurityNamespace += fmt.Sprintf("\x00%x", sha256.Sum256(encoded))
	}
	tls := TLS{InsecureSkipVerify: conn.InsecureTLS}
	switch conn.Type {
	case models.ConnectionTypeGit:
		return gitClient(conn, tls)
	case models.ConnectionTypeGithub, models.ConnectionTypeGitlab, models.ConnectionTypeAzureDevops:
		return providerClient(ctx, conn, tls, clientOptions)
	}
	return nil, fmt.Errorf("connection %q has type %q, which is not a git provider", conn.Name, conn.Type)
}

func gitClient(conn models.Connection, tls TLS) (*Client, error) {
	auth := Auth{Username: conn.Username, Password: conn.Password, PrivateKey: conn.Certificate, TLS: tls}
	repo, err := NewRepository(RepositoryOptions{URL: conn.URL, Auth: auth, ProviderType: models.ConnectionTypeGit})
	if err != nil {
		return nil, fmt.Errorf("git connection %q: %w", conn.Name, err)
	}
	return &Client{Type: conn.Type, Provider: &genericProvider{repo: repo}, Auth: auth}, nil
}

func providerClient(ctx dbcontext.Context, conn models.Connection, tls TLS, options ClientOptions) (*Client, error) {
	base := conn.URL
	if base == "" && conn.Type == models.ConnectionTypeGithub {
		base = githubAPI
	}
	if base == "" {
		return nil, fmt.Errorf("%s API URL is required", conn.Type)
	}
	if strings.TrimSpace(conn.Password) == "" {
		return nil, fmt.Errorf("%s access token is required", conn.Type)
	}
	auth := Auth{Username: conn.Username, Token: conn.Password, TLS: tls}
	authorization := "Bearer " + conn.Password
	if conn.Type == models.ConnectionTypeAzureDevops {
		authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+conn.Password))
	}
	api, err := newRESTClient(ctx, restClientOptions{Provider: conn.Type, BaseURL: base, TLS: tls, Authorization: authorization, Client: options})
	if err != nil {
		return nil, err
	}
	client := &Client{Type: conn.Type, Auth: auth, connectionURL: api.base}
	switch conn.Type {
	case models.ConnectionTypeGithub:
		client.namespace = conn.Properties["organization"]
		client.Provider = &githubProvider{api: api, namespace: client.namespace}
	case models.ConnectionTypeGitlab:
		client.namespace = conn.Properties["group"]
		client.Provider = &gitlabProvider{api: api, namespace: client.namespace}
	default:
		client.namespace = conn.Properties["project"]
		client.Provider = &azureProvider{api: api, project: client.namespace}
	}
	return client, nil
}

// Repository opens rawURL with the connection's credentials and provider,
// refusing a repository outside the connection's host and organization.
func (c *Client) Repository(rawURL string) (*Repository, error) {
	repo, err := NewRepository(RepositoryOptions{URL: rawURL, Auth: c.Auth, Provider: c.Provider})
	if err != nil {
		return nil, err
	}
	if err := c.owns(repo.URL); err != nil {
		return nil, fmt.Errorf("repository %s: %w", repo.URL.Redacted(), err)
	}
	return repo, nil
}

func (c *Client) owns(repo RepoURL) error {
	if strings.EqualFold(repo.Scheme, "http") && !strings.EqualFold(c.connectionScheme(), "http") {
		return fmt.Errorf("scheme %q would send the connection's %s credentials unencrypted: use https", repo.Scheme, c.connectionScheme())
	}
	switch c.Type {
	case models.ConnectionTypeGit:
		generic := c.Provider.(*genericProvider)
		if !strings.EqualFold(hostOnly(repo.Host), hostOnly(generic.repo.URL.Host)) {
			return fmt.Errorf("host %q is not the connection's host %q", repo.Host, generic.repo.URL.Host)
		}
	case models.ConnectionTypeGithub, models.ConnectionTypeGitlab:
		if host := c.webHost(); !strings.EqualFold(hostOnly(repo.Host), host) {
			return fmt.Errorf("host %q is not the connection's host %q", repo.Host, host)
		}
		if c.namespace != "" && !strings.EqualFold(repo.Owner, c.namespace) &&
			!strings.HasPrefix(strings.ToLower(repo.Owner), strings.ToLower(c.namespace)+"/") {
			return fmt.Errorf("owner %q is outside the connection's namespace %q", repo.Owner, c.namespace)
		}
	case models.ConnectionTypeAzureDevops:
		return c.ownsAzure(repo)
	}
	return nil
}

// webHost is the host repositories live on: api.github.com serves github.com,
// any other API (GitHub Enterprise, GitLab) shares its host.
func (c *Client) webHost() string {
	host := strings.ToLower(c.connectionURL.Hostname())
	if host == "api.github.com" {
		return "github.com"
	}
	return host
}

// connectionScheme is the scheme of the URL the connection was configured
// with: the repository of a git connection, the API of a provider.
func (c *Client) connectionScheme() string {
	if generic, ok := c.Provider.(*genericProvider); ok {
		return generic.repo.URL.Scheme
	}
	return c.connectionURL.Scheme
}

// ownsAzure accepts a repository of the connection's organization (and
// project, when set) on Azure DevOps Services when the connection is to
// Services, or on the connection's own host for Azure DevOps Server.
func (c *Client) ownsAzure(repo RepoURL) error {
	repoHost, connHost := strings.ToLower(hostOnly(repo.Host)), strings.ToLower(c.connectionURL.Hostname())
	if isAzureHost(connHost) != isAzureHost(repoHost) || (!isAzureHost(connHost) && repoHost != connHost) {
		return fmt.Errorf("host %q is not the connection's Azure DevOps host %q", repo.Host, c.connectionURL.Host)
	}
	org := azureOrganization(c.connectionURL)
	if !strings.EqualFold(repo.Owner, org) {
		return fmt.Errorf("organization %q is not the connection's organization %q", repo.Owner, org)
	}
	if c.namespace != "" && !strings.EqualFold(repo.Project, c.namespace) {
		return fmt.Errorf("project %q is not the connection's project %q", repo.Project, c.namespace)
	}
	return nil
}

// azureOrganization reads the organization from https://dev.azure.com/{org},
// https://{org}.visualstudio.com or a server's collection path.
func azureOrganization(base *url.URL) string {
	host := strings.ToLower(base.Hostname())
	if strings.HasSuffix(host, ".visualstudio.com") {
		return strings.TrimSuffix(host, ".visualstudio.com")
	}
	return strings.Trim(base.Path, "/")
}
