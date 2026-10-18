package git

import (
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/commons/logger"
	"golang.org/x/crypto/ssh"
)

// configPair is one GIT_CONFIG_KEY_n/GIT_CONFIG_VALUE_n entry.
type configPair struct{ key, value string }

// commandEnv is the environment for a git command that talks to repo: the
// credentials and TLS settings as URL-scoped env config, never argv or a
// config file. cleanup removes the temporary key and certificate files.
func commandEnv(repo *Repository) (env []string, cleanup func(), err error) {
	secrets := &secretFiles{}
	cleanup = secrets.remove
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	pairs := []configPair{{"credential.helper", ""}}
	var extra []string
	switch repo.URL.Scheme {
	case "http", "https":
		httpPairs, err := httpConfig(repo, secrets)
		if err != nil {
			return nil, cleanup, err
		}
		pairs = append(pairs, httpPairs...)
	case "ssh":
		sshCommand, err := sshCommand(repo.Auth, secrets)
		if err != nil {
			return nil, cleanup, err
		}
		extra = append(extra, "GIT_SSH_COMMAND="+sshCommand)
	}
	return append(baseEnv(pairs...), extra...), cleanup, nil
}

// httpConfig scopes Authorization and TLS settings to the repository's
// scheme://host/ so a redirect to another host never receives them.
func httpConfig(repo *Repository, secrets *secretFiles) ([]configPair, error) {
	scope := "http." + repo.URL.Scheme + "://" + repo.URL.Host + "/."
	var pairs []configPair
	if user, password, ok := repo.Auth.basic(repo.URL.Provider); ok {
		header := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
		pairs = append(pairs, configPair{scope + "extraHeader", "Authorization: Basic " + header})
	}
	tls := repo.Auth.TLS
	if tls.InsecureSkipVerify {
		pairs = append(pairs, configPair{scope + "sslVerify", "false"})
	}
	for _, file := range []struct{ key, content string }{{"sslCAInfo", tls.CA}, {"sslCert", tls.Cert}, {"sslKey", tls.Key}} {
		if file.content == "" {
			continue
		}
		path, err := secrets.write(file.key+".pem", []byte(file.content))
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, configPair{scope + file.key, path})
	}
	return pairs, nil
}

// sshCommand runs ssh in batch mode with strict known_hosts checking and,
// when a key is configured, only that key (decrypted into a 0600 file).
func sshCommand(auth Auth, secrets *secretFiles) (string, error) {
	args := []string{"ssh", "-o", "BatchMode=yes"}
	if auth.TLS.InsecureSkipVerify {
		args = append(args, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null")
	} else {
		knownHosts, err := knownHostsPath()
		if err != nil {
			return "", err
		}
		args = append(args, "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+shellQuote(knownHosts))
	}
	if auth.PrivateKey == "" {
		return strings.Join(args, " "), nil
	}
	key, err := unencryptedKey(auth)
	if err != nil {
		return "", err
	}
	path, err := secrets.write("id_key", key)
	if err != nil {
		return "", err
	}
	return strings.Join(append(args, "-o", "IdentitiesOnly=yes", "-i", shellQuote(path)), " "), nil
}

// unencryptedKey returns the key as unencrypted PEM: ssh cannot prompt for a
// passphrase in batch mode.
func unencryptedKey(auth Auth) ([]byte, error) {
	if auth.Password == "" {
		return []byte(auth.PrivateKey), nil
	}
	raw, err := ssh.ParseRawPrivateKeyWithPassphrase([]byte(auth.PrivateKey), []byte(auth.Password))
	if err != nil {
		return nil, fmt.Errorf("decrypt git SSH private key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(raw, "")
	if err != nil {
		return nil, fmt.Errorf("encode git SSH private key: %w", err)
	}
	return pem.EncodeToMemory(block), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// secretFiles is a lazily created 0700 directory of 0600 credential files.
type secretFiles struct{ dir string }

func (s *secretFiles) write(name string, content []byte) (string, error) {
	if s.dir == "" {
		dir, err := os.MkdirTemp("", "commons-db-git-")
		if err != nil {
			return "", fmt.Errorf("create git credential directory: %w", err)
		}
		s.dir = dir
	}
	path := filepath.Join(s.dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return "", fmt.Errorf("write git credential file: %w", err)
	}
	return path, nil
}

func (s *secretFiles) remove() {
	if s.dir == "" {
		return
	}
	if err := os.RemoveAll(s.dir); err != nil {
		logger.Warnf("remove git credential directory %s: %v", s.dir, err)
	}
}
