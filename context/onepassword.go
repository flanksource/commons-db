package context

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// onePasswordTokenProperty is the context property that overrides the
// OP_SERVICE_ACCOUNT_TOKEN environment variable for 1Password lookups.
const onePasswordTokenProperty = "1password.service-account-token"

// opReadFunc resolves a 1Password secret reference to its plaintext value. It is
// a package var so tests can substitute a fake for the `op` CLI.
var opReadFunc = opRead

// onePasswordFingerprintKey makes token fingerprints process-local. The cache
// is also process-local, so fingerprints only need to remain stable for the
// lifetime of this process.
var onePasswordFingerprintKey = []byte(rand.Text())

// GetOnePasswordValueFromCache resolves a 1Password secret reference of the form
// op://<vault>/<item>/<field> to its value. A service-account token (the
// `1password.service-account-token` property, else the OP_SERVICE_ACCOUNT_TOKEN
// environment variable) selects non-interactive auth; without one the ambient
// `op` CLI session is used. Both paths shell out to `op read`.
func GetOnePasswordValueFromCache(ctx Context, ref string) (string, error) {
	token := onePasswordToken(ctx)
	// Scope the cache entry by a token fingerprint (never the token itself) so a
	// value resolved by one service account can't satisfy another's lookup.
	id := fmt.Sprintf("1password/%s/%s", tokenFingerprint(token), ref)
	if value, found := envCache.Get(id); found {
		return value.(string), nil
	}
	value, err, _ := envLookupGroup.Do(id, func() (any, error) {
		v, err := opReadFunc(ctx, ref, token)
		if err != nil {
			return "", err
		}
		envCache.Set(id, v, envCacheTTL(ctx, "envvar.1password.cache.timeout"))
		return v, nil
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

// onePasswordToken resolves the service-account token from context properties,
// falling back to the OP_SERVICE_ACCOUNT_TOKEN environment variable.
func onePasswordToken(ctx Context) string {
	if token := ctx.Properties().String(onePasswordTokenProperty, ""); token != "" {
		return token
	}
	return os.Getenv("OP_SERVICE_ACCOUNT_TOKEN")
}

// tokenFingerprint returns a short, non-reversible identifier for a token so it
// can key a cache entry without exposing the secret. The empty token (ambient
// CLI session) fingerprints to "session".
func tokenFingerprint(token string) string {
	if token == "" {
		return "session"
	}
	digest := hmac.New(sha256.New, onePasswordFingerprintKey)
	_, _ = digest.Write([]byte(token))
	return hex.EncodeToString(digest.Sum(nil)[:8])
}

// opRead invokes `op read` for a single reference. When a token is supplied it
// is injected via OP_SERVICE_ACCOUNT_TOKEN for non-interactive resolution.
func opRead(ctx Context, ref, token string) (string, error) {
	if !strings.HasPrefix(ref, "op://") {
		return "", fmt.Errorf("invalid 1password reference")
	}
	if _, err := exec.LookPath("op"); err != nil {
		return "", fmt.Errorf("1password CLI (op) not found in PATH: %w", err)
	}
	cmd := exec.CommandContext(ctx, "op", "read", "--no-newline", "--", ref)
	cmd.Env = os.Environ()
	if token != "" {
		cmd.Env = append(cmd.Env, "OP_SERVICE_ACCOUNT_TOKEN="+token)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("op read %q failed: %w: %s", ref, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
