// Package keychain keeps a persistent local KMS key in the operating system vault.
package keychain

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flanksource/clicky/text"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/secret"
	"github.com/gofrs/flock"
	"github.com/google/uuid"
	"github.com/zalando/go-keyring"
	"gocloud.dev/secrets"
)

type Options struct {
	Service string
	Account string
	// Confirm is called only for a missing key. Nil means noninteractive.
	Confirm func(context.Context) (bool, error)
}

type document struct {
	Version int                  `json:"version"`
	ID      string               `json:"id"`
	Key     text.SensitiveString `json:"key"`
}

var ErrMissing = errors.New("Keychain KEK missing; run gavel connections kek init interactively on the database host")

// Open loads the existing key or initializes a missing one after confirmation.
func Open(ctx context.Context, options Options) (*secrets.Keeper, string, error) {
	if options.Service == "" || options.Account == "" {
		return nil, "", fmt.Errorf("Keychain service and account are required")
	}
	raw, err := read(options)
	if errors.Is(err, keyring.ErrNotFound) {
		if options.Confirm == nil {
			return nil, "", ErrMissing
		}
		approved, confirmErr := options.Confirm(ctx)
		if confirmErr != nil {
			return nil, "", confirmErr
		}
		if !approved {
			return nil, "", fmt.Errorf("KEK initialization cancelled")
		}
		raw, err = initialize(ctx, options)
	}
	if err != nil {
		return nil, "", fmt.Errorf("read Keychain KEK: %w", err)
	}
	return decode(ctx, raw)
}

func read(options Options) (text.SensitiveString, error) {
	value, err := keyring.Get(options.Service, options.Account)
	return text.NewSensitiveString(value), err
}

func initialize(ctx context.Context, options Options) (text.SensitiveString, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(options.Service + "/" + options.Account))
	dir := filepath.Join(config, "flanksource", "keychain-locks")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	lock := flock.New(filepath.Join(dir, fmt.Sprintf("%x.lock", sum)))
	if err = lock.Lock(); err != nil {
		return "", err
	}
	defer lock.Unlock()
	if err = ctx.Err(); err != nil {
		return "", err
	}
	raw, err := read(options)
	if err == nil {
		return raw, nil
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		return "", err
	}
	key := secret.Sensitive(make([]byte, 32))
	if _, err = rand.Read([]byte(key)); err != nil {
		return "", err
	}
	doc := document{Version: 1, ID: uuid.NewString(), Key: text.NewSensitiveString(base64.URLEncoding.EncodeToString([]byte(key)))}
	data, err := encode(doc)
	if err != nil {
		return "", err
	}
	if err = keyring.Set(options.Service, options.Account, data.Value()); err != nil {
		return "", fmt.Errorf("persist Keychain KEK: %w", err)
	}
	return data, nil
}

func encode(doc document) (text.SensitiveString, error) {
	// The Keychain wire document deliberately contains the recoverable key.
	data, err := json.Marshal(struct {
		Version int    `json:"version"`
		ID      string `json:"id"`
		Key     string `json:"key"`
	}{doc.Version, doc.ID, doc.Key.Value()})
	return text.NewSensitiveString(string(data)), err
}

func decode(ctx context.Context, raw text.SensitiveString) (*secrets.Keeper, string, error) {
	var doc document
	if err := json.Unmarshal([]byte(raw.Value()), &doc); err != nil || doc.Version != 1 || doc.ID == "" {
		return nil, "", fmt.Errorf("invalid Keychain KEK document")
	}
	key, err := base64.URLEncoding.DecodeString(doc.Key.Value())
	if err != nil || len(key) != 32 {
		return nil, "", fmt.Errorf("invalid Keychain KEK: expected 32 bytes")
	}
	keeper, err := secret.KeeperFromConnection(dbcontext.NewContext(ctx), "local_kms://"+doc.Key.Value())
	return keeper, doc.ID, err
}
