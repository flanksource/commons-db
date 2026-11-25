package azure

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"

	commonshttp "github.com/flanksource/commons/http"
	"github.com/flanksource/commons/http/middlewares/azuread"
)

const (
	// AlgRSAOAEP256 is the RSA-OAEP-256 key encryption algorithm.
	AlgRSAOAEP256 = "RSA-OAEP-256"

	keyVaultAPIVersion = "7.4"
)

// KeyVaultOptions configures a KeyVaultClient.
type KeyVaultOptions struct {
	// VaultURL is the vault base URL, e.g. https://name.vault.azure.net/.
	VaultURL   string
	Credential azuread.Credential
	// Scope overrides the token scope, which is otherwise derived from the
	// vault host's cloud suffix (see KeyVaultScope).
	Scope string
	// SkipTLSVerify disables server certificate verification.
	SkipTLSVerify bool
}

// KeyVaultClient performs Key Vault key cryptographic operations.
type KeyVaultClient struct {
	client *commonshttp.Client
	vault  string
}

// KeyOperation names a key and the data an encrypt/decrypt call operates on.
// An empty Version selects the latest key version.
type KeyOperation struct {
	Name      string
	Version   string
	Algorithm string
	Value     []byte
}

// KeyVaultScope returns the token scope for the vault at vaultURL: the host
// minus its vault-name label, so sovereign clouds (vault.usgovcloudapi.net,
// vault.azure.cn) get their own audience.
func KeyVaultScope(vaultURL string) (string, error) {
	u, err := url.Parse(vaultURL)
	if err != nil {
		return "", fmt.Errorf("invalid vault URL %q: %w", vaultURL, err)
	}
	_, suffix, ok := strings.Cut(u.Hostname(), ".")
	if !ok || suffix == "" {
		return "", fmt.Errorf("cannot derive the token scope from vault URL %q: host has no cloud suffix", vaultURL)
	}
	return "https://" + suffix + "/.default", nil
}

// NewKeyVaultClient builds a client for one vault.
func NewKeyVaultClient(opts KeyVaultOptions) (*KeyVaultClient, error) {
	if opts.VaultURL == "" {
		return nil, errors.New("azure: vault URL is required")
	}
	scope := opts.Scope
	if scope == "" {
		derived, err := KeyVaultScope(opts.VaultURL)
		if err != nil {
			return nil, err
		}
		scope = derived
	}
	client, err := newClient(opts.Credential, scope, opts.SkipTLSVerify)
	if err != nil {
		return nil, err
	}
	return &KeyVaultClient{client: client, vault: strings.TrimRight(opts.VaultURL, "/")}, nil
}

// Encrypt encrypts op.Value with the key and returns the ciphertext.
func (c *KeyVaultClient) Encrypt(ctx context.Context, op KeyOperation) ([]byte, error) {
	return c.operate(ctx, "encrypt", op)
}

// Decrypt decrypts op.Value with the key and returns the plaintext.
func (c *KeyVaultClient) Decrypt(ctx context.Context, op KeyOperation) ([]byte, error) {
	return c.operate(ctx, "decrypt", op)
}

type keyOperationBody struct {
	Algorithm string `json:"alg"`
	Value     string `json:"value"`
}

func (c *KeyVaultClient) operate(ctx context.Context, action string, op KeyOperation) ([]byte, error) {
	if op.Name == "" {
		return nil, errors.New("azure: key name is required")
	}
	if op.Algorithm == "" {
		return nil, errors.New("azure: key algorithm is required")
	}
	// An empty version leaves "//", which Key Vault resolves to the latest
	// version (the same request the Azure SDK produced).
	endpoint := fmt.Sprintf("%s/keys/%s/%s/%s", c.vault, url.PathEscape(op.Name), url.PathEscape(op.Version), action)
	in := keyOperationBody{Algorithm: op.Algorithm, Value: base64.RawURLEncoding.EncodeToString(op.Value)}
	var out keyOperationBody
	if err := postJSON(ctx, c.client, endpoint, url.Values{"api-version": {keyVaultAPIVersion}}, in, &out); err != nil {
		return nil, err
	}
	result, err := base64.RawURLEncoding.DecodeString(out.Value)
	if err != nil {
		return nil, fmt.Errorf("decoding key vault %s result: %w", action, err)
	}
	return result, nil
}
