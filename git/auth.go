package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/models"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gogitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// TLS customizes HTTPS verification: an extra CA bundle (added to the system
// pool), a client certificate for mutual TLS, or no verification at all.
// InsecureSkipVerify also disables SSH host-key checking.
type TLS struct {
	CA                 string
	Cert               string
	Key                string
	InsecureSkipVerify bool
}

// Auth carries a repository's credentials. HTTP(S) uses Username/Password
// or Token (sent as Basic auth with the provider's token user); SSH uses
// PrivateKey, with Password as its passphrase.
type Auth struct {
	Username   string
	Password   string
	Token      string
	PrivateKey string
	TLS        TLS
}

// Validate rejects ambiguous or malformed credentials.
func (a Auth) Validate() error {
	if a.Password != "" && a.Token != "" {
		return errors.New("git auth has both a password and a token: set one")
	}
	if (a.TLS.Cert == "") != (a.TLS.Key == "") {
		return errors.New("git TLS client certificate and key must be set together")
	}
	if a.PrivateKey != "" {
		if _, err := a.signer(); err != nil {
			return err
		}
	}
	return nil
}

// basic returns the HTTP Basic credentials, if any. A token is sent with the
// provider's token user unless Username is set.
func (a Auth) basic(provider string) (user, password string, ok bool) {
	if a.Token != "" {
		user = a.Username
		if user == "" {
			user = tokenUser(provider)
		}
		return user, a.Token, true
	}
	return a.Username, a.Password, a.Username != "" || a.Password != ""
}

// tokenUser is the Basic-auth user each provider expects alongside a PAT.
// Azure DevOps ignores the user, so it is left empty (":PAT").
func tokenUser(provider string) string {
	switch provider {
	case models.ConnectionTypeGithub:
		return "x-access-token"
	case models.ConnectionTypeGitlab:
		return "oauth2"
	default:
		return ""
	}
}

func (a Auth) signer() (ssh.Signer, error) {
	var (
		signer ssh.Signer
		err    error
	)
	if a.Password != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(a.PrivateKey), []byte(a.Password))
	} else {
		signer, err = ssh.ParsePrivateKey([]byte(a.PrivateKey))
	}
	if err != nil {
		return nil, fmt.Errorf("parse git SSH private key: %w", err)
	}
	return signer, nil
}

// goGitAuth is the go-git auth method for LsRemote.
func (a Auth) goGitAuth(u RepoURL) (transport.AuthMethod, error) {
	switch u.Scheme {
	case "http", "https":
		if user, password, ok := a.basic(u.Provider); ok {
			return &gogithttp.BasicAuth{Username: user, Password: password}, nil
		}
	case "ssh":
		if a.PrivateKey == "" {
			return nil, nil
		}
		signer, err := a.signer()
		if err != nil {
			return nil, err
		}
		callback, err := a.hostKeyCallback()
		if err != nil {
			return nil, err
		}
		return &gogitssh.PublicKeys{User: sshUser(u), Signer: signer,
			HostKeyCallbackHelper: gogitssh.HostKeyCallbackHelper{HostKeyCallback: callback}}, nil
	}
	return nil, nil
}

func sshUser(u RepoURL) string {
	if u.User != "" {
		return u.User
	}
	return "git"
}

func (a Auth) hostKeyCallback() (ssh.HostKeyCallback, error) {
	if a.TLS.InsecureSkipVerify {
		return ssh.InsecureIgnoreHostKey(), nil //nolint:gosec // explicit per-connection opt-in
	}
	path, err := knownHostsPath()
	if err != nil {
		return nil, err
	}
	callback, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("load SSH known_hosts %s: %w", path, err)
	}
	return callback, nil
}

// knownHostsPath is $SSH_KNOWN_HOSTS, else ~/.ssh/known_hosts.
func knownHostsPath() (string, error) {
	if path := os.Getenv("SSH_KNOWN_HOSTS"); path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate SSH known_hosts: %w", err)
	}
	return filepath.Join(home, ".ssh", "known_hosts"), nil
}
