package context

import (
	"context"
	"testing"

	commons "github.com/flanksource/commons/context"
	"github.com/flanksource/commons/properties"
)

func newOnePasswordTestContext() Context {
	return Context{Context: commons.NewContext(context.Background())}
}

func TestGetOnePasswordValueFromCache(t *testing.T) {
	envCache.Flush()
	ref := "op://prod/postgres/password"

	var calls int
	opReadFunc = func(_ Context, gotRef, _ string) (string, error) {
		calls++
		if gotRef != ref {
			t.Fatalf("op read received %q, want %q", gotRef, ref)
		}
		return "s3cr3t", nil
	}
	t.Cleanup(func() { opReadFunc = opRead })

	ctx := newOnePasswordTestContext()
	got, err := GetOnePasswordValueFromCache(ctx, ref)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if got != "s3cr3t" {
		t.Fatalf("resolved value = %q, want %q", got, "s3cr3t")
	}

	// A second lookup is served from the cache, not a fresh `op read`.
	if _, err := GetOnePasswordValueFromCache(ctx, ref); err != nil {
		t.Fatalf("cached resolve failed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected op read to run once (cached thereafter), ran %d times", calls)
	}
}

func TestOpReadRejectsNonSecretReferences(t *testing.T) {
	if _, err := opRead(newOnePasswordTestContext(), "--help", ""); err == nil {
		t.Fatal("expected a non-op reference to be rejected")
	}
}

func TestOnePasswordTokenPrefersProperty(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "env-token")

	ctx := newOnePasswordTestContext()
	if got := onePasswordToken(ctx); got != "env-token" {
		t.Fatalf("without a property, token should fall back to the env var, got %q", got)
	}

	properties.Set(onePasswordTokenProperty, "prop-token")
	ctx.ClearCache()
	t.Cleanup(func() {
		properties.Set(onePasswordTokenProperty, "")
		ctx.ClearCache()
	})
	if got := onePasswordToken(ctx); got != "prop-token" {
		t.Fatalf("the property should take precedence over the env var, got %q", got)
	}
}

func TestTokenFingerprintIsolatesCache(t *testing.T) {
	const (
		tokenA                      = "a"
		tokenB                      = "b"
		unkeyedFingerprintForTokenA = "ca978112ca1bbdca"
	)

	// Different tokens must not collide in the cache key; the empty (session)
	// token has its own stable fingerprint.
	if tokenFingerprint("") != "session" {
		t.Fatalf("empty token should fingerprint to session, got %q", tokenFingerprint(""))
	}
	fingerprintA := tokenFingerprint(tokenA)
	if fingerprintA != tokenFingerprint(tokenA) {
		t.Fatal("a token fingerprint must remain stable for the process lifetime")
	}
	if fingerprintA == tokenFingerprint(tokenB) {
		t.Fatal("distinct tokens must produce distinct fingerprints")
	}
	if fingerprintA == "" {
		t.Fatal("a non-empty token must produce a non-empty fingerprint")
	}
	if fingerprintA == unkeyedFingerprintForTokenA {
		t.Fatal("token fingerprint must not be an unkeyed digest of the sensitive token")
	}
}
