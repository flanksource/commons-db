package connection

import (
	"fmt"
	"net/url"

	"github.com/flanksource/commons-db/context"
	dbgit "github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/types"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons/utils"
)

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
	// Since is a commit-ish whose merge-base diff against HEAD is folded into
	// the reported dirtyFiles. Informational only.
	Since string `yaml:"since,omitempty" json:"since,omitempty"`
	// Deprecated: no-op, retained so existing configs keep parsing. Use
	// GitWorktree.Uncommitted / GitWorktree.Ignored and Since instead.
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
	// Existing checks Branch out as it is instead of creating it from Base; the
	// branch must already exist.
	Existing bool   `yaml:"existing,omitempty" json:"existing,omitempty"`
	Base     string `yaml:"base,omitempty" json:"base,omitempty"`
	Path     string `yaml:"path,omitempty" json:"path,omitempty"`
	Keep     bool   `yaml:"keep,omitempty" json:"keep,omitempty"`
	// Uncommitted carries staged, unstaged and untracked changes from the
	// source repo into the worktree. The source is never mutated.
	Uncommitted bool `yaml:"uncommitted,omitempty" json:"uncommitted,omitempty"`
	// Ignored copies gitignored content into the worktree.
	Ignored bool `yaml:"ignored,omitempty" json:"ignored,omitempty"`
}

func (w *GitWorktree) IsEnabled() bool {
	return w != nil && (w.Enabled || w.Prefix != "" || w.Branch != "" || w.Base != "" || w.Path != "" || w.Keep ||
		w.Existing || w.Uncommitted || w.Ignored)
}

// Deprecated: no-op. See GitConnection.Dirty.
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

// Repository opens the connection's remote with its credentials. Call
// HydrateConnection first so every EnvVar holds its resolved value.
func (c GitConnection) Repository() (*dbgit.Repository, error) {
	auth := dbgit.Auth{
		Username:   c.GetUsername().ValueStatic,
		Password:   c.GetPassword().ValueStatic,
		PrivateKey: c.GetCertificate().ValueStatic,
		TLS: dbgit.TLS{CA: c.TLS.CA.ValueStatic, Cert: c.TLS.Cert.ValueStatic, Key: c.TLS.Key.ValueStatic,
			InsecureSkipVerify: c.TLS.InsecureSkipVerify},
	}
	repo, err := dbgit.NewRepository(dbgit.RepositoryOptions{URL: c.URL, Auth: auth, ProviderType: c.Type})
	if err != nil {
		return nil, fmt.Errorf("git connection: %w", err)
	}
	return repo, nil
}

// CloneOptions is the checkout this connection asks for in dir: the URL's
// ?ref= beats Branch and ?depth= beats Depth; an unset depth is 1 and an
// unset ref is the remote HEAD.
func (c GitConnection) CloneOptions(dir string) (dbgit.CloneOptions, error) {
	parsed, err := dbgit.ParseRepoURL(c.URL, c.Type)
	if err != nil {
		return dbgit.CloneOptions{}, fmt.Errorf("git connection: %w", err)
	}
	options := dbgit.CloneOptions{Dir: dir, Ref: c.Branch, Depth: 1}
	if c.Depth != nil {
		options.Depth = *c.Depth
	}
	if parsed.Ref != "" {
		options.Ref = parsed.Ref
	}
	if parsed.Depth != nil {
		options.Depth = *parsed.Depth
	}
	return options, nil
}
