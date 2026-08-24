package app

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	queryConfigDirEnv = "QUERY_CONFIG_DIR"
	queryDataDirEnv   = "QUERY_DATA_DIR"
	queryDBURLEnv     = "QUERY_DB_URL"
)

// EmbeddedDatabase is the --db value that resolves to the embedded PostgreSQL
// cluster under the data dir — the same store `query serve` and the web UI use.
const EmbeddedDatabase = "embedded"

// defaultQueryConfigDir follows XDG on every platform: an explicit
// XDG_CONFIG_HOME wins, otherwise state lives below ~/.config.
func defaultQueryConfigDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "flanksource", "query")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".config", "flanksource", "query")
	}
	return filepath.Join(home, ".config", "flanksource", "query")
}

func resolveConfigDir(args []string) string {
	if value, ok := stringFlag(args, "--config-dir"); ok {
		return value
	}
	if value := os.Getenv(queryConfigDirEnv); value != "" {
		return value
	}
	return defaultQueryConfigDir()
}

func ResolveConfigDir(args []string) string { return resolveConfigDir(args) }

func ResolveProfilesDir(args []string) string {
	if value, ok := stringFlag(args, "--profiles-dir"); ok {
		return value
	}
	if value := os.Getenv("QUERY_PROFILES_DIR"); value != "" {
		return value
	}
	return filepath.Join(resolveConfigDir(args), "profiles")
}

// ResolveDBURL reads --db before cobra parses, because the profile store the
// command tree is generated from has to exist first. An explicitly empty value
// is not the same as an absent one: it selects the YAML file store.
func ResolveDBURL(args []string) string {
	if value, ok := stringFlag(args, "--db"); ok {
		return value
	}
	if value := os.Getenv(queryDBURLEnv); value != "" {
		return value
	}
	return EmbeddedDatabase
}

func ResolveDataDir(args []string) string {
	explicit, _ := stringFlag(args, "--data-dir")
	return resolveDataDir(resolveConfigDir(args), explicit)
}

func stringFlag(args []string, name string) (string, bool) {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1], true
		}
		// `--flag=` is an explicitly empty value, not an absent flag: --db= is how
		// a sub-command opts out of the database.
		if value, found := strings.CutPrefix(arg, name+"="); found {
			return value, true
		}
	}
	return "", false
}

func NormalizeConfigDir(value string) string {
	if strings.TrimSpace(value) == "" {
		return defaultQueryConfigDir()
	}
	return value
}

func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

func resolveDataDir(configDir, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if value := os.Getenv(queryDataDirEnv); value != "" {
		return value
	}
	return filepath.Join(configDir, "postgres")
}
