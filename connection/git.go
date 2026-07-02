package connection

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	netHTTP "net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons/utils"
	"github.com/samber/lo"

	git "github.com/go-git/go-git/v5"

	"github.com/flanksource/commons-db/types"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitClient "github.com/go-git/go-git/v5/plumbing/transport/client"
	gitHTTP "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	ssh2 "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

var gitHTTPTransportMu sync.Mutex

const (
	ServiceGithub = "github"
	ServiceGitlab = "gitlab"
)

type GitClient struct {
	Auth                transport.AuthMethod
	URL                 string
	Owner, Repo, Branch string
	Depth               int
	AzureDevops         bool
	// TLS is the git-over-HTTPS transport config (custom CA, client
	// certificate, insecure-skip-verify). nil leaves go-git's default TLS.
	TLS *tls.Config
}

// RedactedURL returns the URL with userinfo elided, falling back to the raw URL
// when it doesn't parse.
func (gitClient GitClient) RedactedURL() string {
	if uri, err := url.Parse(gitClient.URL); err == nil {
		return uri.Redacted()
	}
	return gitClient.URL
}

func (gitClient GitClient) GetContext() map[string]any {
	if uri, err := url.Parse(gitClient.URL); err == nil {
		return map[string]any{
			"url":    uri.Redacted(),
			"branch": gitClient.Branch,
		}
	}
	return map[string]any{
		"url":    "redacted",
		"owner":  gitClient.Owner,
		"repo":   gitClient.Repo,
		"branch": gitClient.Branch,
	}
}

func (gitClient GitClient) GetShortURL() string {
	u, err := url.Parse(gitClient.URL)
	if err != nil {
		return ""
	}
	u.Scheme = ""
	u.RawQuery = ""
	return strings.TrimLeft(u.Redacted(), "/")
}

func (gitClient GitClient) LoggerName() string {
	if gitClient.Branch != "" && gitClient.Branch != "main" {
		return fmt.Sprintf("%s@%s", gitClient.GetShortURL(), gitClient.Branch)
	}
	return gitClient.GetShortURL()
}

// configureGitHTTPTransport installs an auth/TLS-aware, logging HTTP transport
// into go-git's process-global protocol registry for the duration of a
// clone/fetch. It applies the connection's TLS settings (custom CA, client
// certificate, insecure-skip-verify) and wraps the transport with
// ApplyHTTPObservability so git-over-HTTP traffic is logged/redacted at the
// "git" feature's effective level (BasicAuth is applied by go-git from
// CloneOptions.Auth). It returns a restore func that reinstalls go-git's
// default HTTP client.
//
// The registry is process-wide, so gitHTTPTransportMu serialises concurrent
// HTTP clones: the lock is held until the returned restore func runs (callers
// defer it), guaranteeing the default client is put back and never left pointing
// at one clone's transport for another's request. Non-HTTP schemes
// (ssh/file/git) need no HTTP transport and get a no-op restore.
func configureGitHTTPTransport(ctx context.Context, rawURL string, tlsConfig *tls.Config) func() {
	scheme := "https"
	if u, err := url.Parse(rawURL); err == nil && u.Scheme != "" {
		scheme = strings.ToLower(u.Scheme)
	}
	if scheme != "http" && scheme != "https" {
		return func() {}
	}

	gitHTTPTransportMu.Lock()

	base := netHTTP.DefaultTransport.(*netHTTP.Transport).Clone()
	if tlsConfig != nil {
		base.TLSClientConfig = tlsConfig
	}
	httpClient := &netHTTP.Client{
		Transport: ApplyHTTPObservability(ctx, "git", base, nil),
	}
	gitClient.InstallProtocol(scheme, gitHTTP.NewClient(httpClient))

	return func() {
		gitClient.InstallProtocol(scheme, gitHTTP.DefaultClient)
		gitHTTPTransportMu.Unlock()
	}
}

func (gitClient *GitClient) Clone(ctx context.Context, dir string) (map[string]any, error) {

	if gitClient.AzureDevops {
		transport.UnsupportedCapabilities = []capability.Capability{
			capability.ThinPack,
		}
	}

	ctx = ctx.WithObject(*gitClient)
	var gitLog logger.Logger = logger.GetLogger("git")
	if headers, bodies := ctx.HTTPLoggingContent("git"); bodies {
		gitLog = gitLog.WithV(logger.Trace)
	} else if headers {
		gitLog = gitLog.WithV(logger.Debug)
	}
	progress := gitLog.V(2)
	restoreGitTransport := configureGitHTTPTransport(ctx, gitClient.URL, gitClient.TLS)
	defer restoreGitTransport()

	redactedURL := gitClient.RedactedURL()
	gitLog.V(1).Infof("clone url=%s branch=%s depth=%d dir=%s", redactedURL, gitClient.Branch, gitClient.Depth, dir)
	extra := map[string]any{
		"git": gitClient.GetShortURL(),
	}

	repo, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL:           gitClient.URL,
		Progress:      progress,
		Auth:          gitClient.Auth,
		ReferenceName: plumbing.NewBranchReferenceName(gitClient.Branch),
		Depth:         gitClient.Depth,
	})

	if errors.Is(err, git.ErrRepositoryAlreadyExists) {
		repo, err = git.PlainOpen(dir)
		if err != nil {
			return extra, ctx.Oops().Wrapf(err, "unable to open repository")
		}

		tree, err := repo.Worktree()
		if err != nil {
			return extra, ctx.Oops().Wrapf(err, "unable to open worktree")
		}

		gitLog.V(1).Infof("fetch url=%s branch=%s depth=%d", redactedURL, gitClient.Branch, gitClient.Depth)
		if err := repo.FetchContext(ctx, &git.FetchOptions{
			Progress:  progress,
			RemoteURL: gitClient.URL,
			Force:     true,
			Prune:     true,
			Auth:      gitClient.Auth,
			Depth:     gitClient.Depth}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return extra, ctx.Oops().Wrapf(err, "error during git fetch")
		}

		refName := plumbing.NewRemoteReferenceName("origin", gitClient.Branch)
		if remote, err := repo.Remote("origin"); err == nil {
			list, err := remote.List(&git.ListOptions{
				Auth: gitClient.Auth,
			})
			if err != nil {
				return extra, ctx.Oops().Wrapf(err, "error during git remote ls")
			}

			for _, ref := range list {
				if ref.Name().Short() == gitClient.Branch {
					refName = ref.Name()
					gitLog.V(2).Infof("found ref %s matching %s", refName, gitClient.Branch)
				}
			}

		}

		if err := tree.Checkout(&git.CheckoutOptions{
			Branch: refName,
			Force:  true,
		}); err != nil {
			return extra, ctx.Oops().Wrapf(err, "error during git checkout")
		}
	} else if err != nil {
		return extra, ctx.Oops().Wrapf(err, "error during git clone")
	}

	if commit, err := repo.Head(); err != nil {
		return extra, ctx.Oops().Wrapf(err, "unable to get HEAD commit")
	} else {
		extra["commit"] = commit.Hash().String()

		if iter, err := repo.Log(&git.LogOptions{From: commit.Hash()}); err == nil {
			if commit, err := iter.Next(); err != nil {
				return extra, ctx.Oops().Wrapf(err, "unable to get HEAD commit")
			} else {
				gitLog.Infof("checked out %s dir=%s", commit.Hash.String()[0:8], dir)
			}
		}
	}

	return extra, nil
}

// +kubebuilder:object:generate=true
type GitConnection struct {
	URL         string        `yaml:"url,omitempty" json:"url,omitempty"`
	Path        string        `yaml:"path,omitempty" json:"path,omitempty"`
	Connection  string        `yaml:"connection,omitempty" json:"connection,omitempty"`
	Username    *types.EnvVar `yaml:"username,omitempty" json:"username,omitempty"`
	Password    *types.EnvVar `yaml:"password,omitempty" json:"password,omitempty"`
	Certificate *types.EnvVar `yaml:"certificate,omitempty" json:"certificate,omitempty"`
	// TLS configures custom CA, client certificate and insecure-skip-verify for
	// git-over-HTTPS. InsecureSkipVerify also skips SSH host-key verification.
	TLS TLSConfig `yaml:"tls,omitempty" json:"tls,omitempty"`
	// Type of connection e.g. github, gitlab
	Type   string `yaml:"type,omitempty" json:"type,omitempty"`
	Branch string `yaml:"branch,omitempty" json:"branch,omitempty"`
	Depth  *int   `yaml:"depth,omitempty" json:"depth,omitempty"`
	// Worktree creates a native git worktree for the checkout before running.
	Worktree *GitWorktree `yaml:"worktree,omitempty" json:"worktree,omitempty"`
	// Dirty selects local staged/unstaged/untracked changes to copy into a
	// temporary worktree when Path is used.
	Dirty *GitDirty `yaml:"dirty,omitempty" json:"dirty,omitempty"`
	// Destination is the full path to where the contents of the URL should be downloaded to.
	// If left empty, the sha256 hash of the URL will be used as the dir name.
	Destination *string `yaml:"destination,omitempty" json:"destination,omitempty"`
}

// +kubebuilder:object:generate=true
type GitWorktree struct {
	Enabled bool   `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Prefix  string `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	Branch  string `yaml:"branch,omitempty" json:"branch,omitempty"`
	Base    string `yaml:"base,omitempty" json:"base,omitempty"`
	Path    string `yaml:"path,omitempty" json:"path,omitempty"`
	Keep    bool   `yaml:"keep,omitempty" json:"keep,omitempty"`
}

func (w *GitWorktree) IsEnabled() bool {
	return w != nil && (w.Enabled || w.Prefix != "" || w.Branch != "" || w.Base != "" || w.Path != "" || w.Keep)
}

// +kubebuilder:object:generate=true
type GitDirty struct {
	Staged    bool   `yaml:"staged,omitempty" json:"staged,omitempty"`
	Unstaged  bool   `yaml:"unstaged,omitempty" json:"unstaged,omitempty"`
	Untracked bool   `yaml:"untracked,omitempty" json:"untracked,omitempty"`
	Since     string `yaml:"since,omitempty" json:"since,omitempty"`
}

func (d *GitDirty) IsEmpty() bool {
	return d == nil || (!d.Staged && !d.Unstaged && !d.Untracked && d.Since == "")
}

func (git GitConnection) GetURL() types.EnvVar {
	return types.EnvVar{ValueStatic: git.URL}
}

func (git GitConnection) GetUsername() types.EnvVar {
	return utils.Deref(git.Username)
}

func (git GitConnection) GetPassword() types.EnvVar {
	return utils.Deref(git.Password)
}

func (git GitConnection) GetCertificate() types.EnvVar {
	return utils.Deref(git.Certificate)
}

func (c *GitConnection) HydrateConnection(ctx context.Context) error {
	ctx.Logger.V(9).Infof("Hydrating GitConnection %s", logger.Pretty(*c))

	if c.Connection != "" {
		conn, err := ctx.HydrateConnectionByURL(c.Connection)
		if err != nil {
			return err
		}
		if conn != nil {
			if (c.Username == nil || c.Username.IsEmpty()) && conn.Username != "" {
				c.Username = &types.EnvVar{ValueStatic: conn.Username}
			}
			if (c.Password == nil || c.Password.IsEmpty()) && conn.Password != "" {
				c.Password = &types.EnvVar{ValueStatic: conn.Password}
			}
			if (c.Certificate == nil || c.Certificate.IsEmpty()) && conn.Certificate != "" {
				c.Certificate = &types.EnvVar{ValueStatic: conn.Certificate}
			}
			if c.URL == "" {
				c.URL = conn.URL
			}
		}
	}

	if c.URL == "" && c.Path != "" {
		// Local paths do not need URL normalization or remote auth.
	} else if uri, err := url.Parse(c.URL); err == nil {
		if uri.Scheme == "" {
			uri.Scheme = "https"
			c.URL = uri.String()
		}
	}

	if c.Username == nil {
		c.Username = &types.EnvVar{}
	}

	if c.Password == nil {
		c.Password = &types.EnvVar{}
	}

	if c.Certificate == nil {
		c.Certificate = &types.EnvVar{}
	}

	if username, err := ctx.GetEnvValueFromCache(*c.Username, ctx.GetNamespace()); err != nil {
		return fmt.Errorf("could not parse username: %v", err)
	} else if username != "" {
		c.Username.ValueStatic = username
	}

	if password, err := ctx.GetEnvValueFromCache(*c.Password, ctx.GetNamespace()); err != nil {
		return fmt.Errorf("could not parse password: %w", err)
	} else if password != "" {
		c.Password.ValueStatic = password
	}

	if certificate, err := ctx.GetEnvValueFromCache(*c.Certificate, ctx.GetNamespace()); err != nil {
		return fmt.Errorf("could not parse certificate: %v", err)
	} else if certificate != "" {
		c.Certificate.ValueStatic = certificate
	}

	if ca, err := ctx.GetEnvValueFromCache(c.TLS.CA, ctx.GetNamespace()); err != nil {
		return fmt.Errorf("could not parse tls ca: %w", err)
	} else if ca != "" {
		c.TLS.CA.ValueStatic = ca
	}
	if cert, err := ctx.GetEnvValueFromCache(c.TLS.Cert, ctx.GetNamespace()); err != nil {
		return fmt.Errorf("could not parse tls cert: %w", err)
	} else if cert != "" {
		c.TLS.Cert.ValueStatic = cert
	}
	if key, err := ctx.GetEnvValueFromCache(c.TLS.Key, ctx.GetNamespace()); err != nil {
		return fmt.Errorf("could not parse tls key: %w", err)
	} else if key != "" {
		c.TLS.Key.ValueStatic = key
	}

	ctx.Logger.V(9).Infof("Hydrated GitConnection %s", logger.Pretty(*c))

	return nil
}

func CreateGitConfig(ctx context.Context, conn *GitConnection) (*GitClient, error) {
	config := &GitClient{
		URL:    conn.URL,
		Depth:  lo.Ternary(conn.Depth == nil, 1, lo.FromPtr(conn.Depth)),
		Branch: lo.CoalesceOrEmpty(conn.Branch, "main"),
	}

	if uri, err := url.Parse(conn.URL); err == nil {
		if ref := uri.Query().Get("ref"); ref != "" {
			config.Branch = ref
		}
		if depth := uri.Query().Get("depth"); depth != "" {
			depthInt, err := strconv.Atoi(depth)
			if err != nil {
				return nil, err
			}
			config.Depth = depthInt
		}
		// strip of any query parameters
		uri.RawQuery = ""
		config.URL = uri.String()
	}

	if owner, repo, ok := parseGenericRepoURL(conn.URL, "github.com", false); ok {
		config.Owner = owner
		config.Repo = repo
	} else if owner, repo, ok := parseGenericRepoURL(conn.URL, "gitlab.com", conn.Type == ServiceGitlab); ok {
		config.Owner = owner
		config.Repo = repo
	} else if azureOrg, azureProject, repo, ok := parseAzureDevopsRepo(conn.URL); ok {
		config.Owner = fmt.Sprintf("%s/%s", azureOrg, azureProject)
		config.Repo = repo
		config.AzureDevops = true
	}
	if strings.HasPrefix(conn.URL, "ssh://") {
		sshURL := conn.URL[6:]
		user := strings.Split(sshURL, "@")[0]

		publicKeys, err := ssh.NewPublicKeys(user, []byte(conn.Certificate.ValueStatic), conn.Password.ValueStatic)
		if err != nil {
			return nil, ctx.Oops().Wrapf(err, "failed to create public keys")
		}
		if conn.TLS.InsecureSkipVerify {
			publicKeys.HostKeyCallback = ssh2.InsecureIgnoreHostKey()
		} else if publicKeys.HostKeyCallback, err = gitKnownHostKeyCallback(); err != nil {
			return nil, ctx.Oops().Wrapf(err, "failed to load SSH known_hosts")
		}
		config.Auth = publicKeys

	} else {
		config.Auth = &gitHTTP.BasicAuth{
			Username: conn.Username.ValueStatic,
			Password: conn.Password.ValueStatic,
		}
		tlsConfig, err := buildGitHTTPSTLSConfig(conn.TLS)
		if err != nil {
			return nil, ctx.Oops().Wrapf(err, "failed to configure git TLS")
		}
		config.TLS = tlsConfig
	}

	return config, nil
}

// buildGitHTTPSTLSConfig builds a *tls.Config for git-over-HTTPS from the
// connection's TLS settings: a custom CA (RootCAs), a client certificate for
// mutual TLS, and/or insecure-skip-verify. Returns nil when no TLS
// customisation is requested, leaving go-git's default transport in place.
func buildGitHTTPSTLSConfig(t TLSConfig) (*tls.Config, error) {
	ca := t.CA.ValueStatic
	cert := t.Cert.ValueStatic
	key := t.Key.ValueStatic
	if !t.InsecureSkipVerify && ca == "" && cert == "" && key == "" {
		return nil, nil
	}

	cfg := &tls.Config{InsecureSkipVerify: t.InsecureSkipVerify} //nolint:gosec // insecure is an explicit per-connection opt-in

	if ca != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(ca)) {
			return nil, fmt.Errorf("invalid PEM CA certificate")
		}
		cfg.RootCAs = pool
	}

	if cert != "" && key != "" {
		pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
		if err != nil {
			return nil, fmt.Errorf("invalid client certificate/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}

	return cfg, nil
}

func gitKnownHostKeyCallback() (ssh2.HostKeyCallback, error) {
	knownHostsPath := os.Getenv("SSH_KNOWN_HOSTS")
	if knownHostsPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		knownHostsPath = filepath.Join(home, ".ssh", "known_hosts")
	}
	return knownhosts.New(knownHostsPath)
}

var azureDevopsRepoURLRegexp = regexp.MustCompile(`^https:\/\/[a-zA-Z0-9_-]+@dev\.azure\.com\/([a-zA-Z0-9_-]+)\/([a-zA-Z0-9_-]+)\/_git\/([a-zA-Z0-9_-]+)`)

func parseAzureDevopsRepo(url string) (org, project, repo string, ok bool) {
	matches := azureDevopsRepoURLRegexp.FindStringSubmatch(url)
	if len(matches) != 4 {
		return "", "", "", false
	}

	return matches[1], matches[2], matches[3], true
}

// parseGenericRepoURL parses a URL into owner and repo.
//   - custom: true if the repo has custom domain
func parseGenericRepoURL(repoURL, host string, custom bool) (owner string, repo string, ok bool) {
	parsed, err := url.Parse(repoURL)
	if err != nil {
		return "", "", false
	}

	if !custom && parsed.Hostname() != host {
		return "", "", false
	}

	path := strings.TrimSuffix(parsed.Path, ".git")
	path = strings.TrimPrefix(path, "/")
	paths := strings.Split(path, "/")
	if len(paths) != 2 {
		return "", "", false
	}

	return paths[0], paths[1], true
}
