package keeper

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/nacl/secretbox"
)

const nonceSize = 24

// Local encrypts with NaCl secretbox under a 32-byte key. Its ciphertext is
// the random nonce followed by the sealed message, byte-compatible with
// gocloud.dev/secrets/localsecrets, so data written by the previous keeper
// still decrypts.
type Local struct {
	key [32]byte
}

// NewLocalFromBase64 builds a Local keeper from a url-safe base64 32-byte key.
func NewLocalFromBase64(encoded string) (*Local, error) {
	if encoded == "" {
		return nil, errors.New("local keeper: key is empty")
	}
	raw, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("local keeper: key is not url-safe base64: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("local keeper: key is %d bytes, want 32 bytes", len(raw))
	}
	return NewLocal([32]byte(raw)), nil
}

// NewLocal builds a Local keeper from raw 32-byte key material.
func NewLocal(key [32]byte) *Local {
	return &Local{key: key}
}

func (l *Local) Encrypt(_ context.Context, plaintext []byte) ([]byte, error) {
	var nonce [nonceSize]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, fmt.Errorf("local keeper: generating nonce: %w", err)
	}
	return secretbox.Seal(nonce[:], plaintext, &nonce, &l.key), nil
}

func (l *Local) Decrypt(_ context.Context, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("local keeper: ciphertext is %d bytes, shorter than the %d-byte nonce", len(ciphertext), nonceSize)
	}
	var nonce [nonceSize]byte
	copy(nonce[:], ciphertext[:nonceSize])
	plaintext, ok := secretbox.Open(nil, ciphertext[nonceSize:], &nonce, &l.key)
	if !ok {
		return nil, errors.New("local keeper: decryption authentication failed (wrong key or tampered ciphertext)")
	}
	return plaintext, nil
}

func (l *Local) Close() error { return nil }
