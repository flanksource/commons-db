package app

import (
	"os"
	"path/filepath"
	"testing"
)

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func TestDefaultQueryConfigDirUsesXDGThenHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	if got, want := defaultQueryConfigDir(), filepath.Join(root, "xdg", "flanksource", "query"); got != want {
		t.Fatalf("XDG config dir = %q, want %q", got, want)
	}

	unsetEnv(t, "XDG_CONFIG_HOME")
	t.Setenv("HOME", filepath.Join(root, "home"))
	if got, want := defaultQueryConfigDir(), filepath.Join(root, "home", ".config", "flanksource", "query"); got != want {
		t.Fatalf("home config dir = %q, want %q", got, want)
	}
}

func TestConfigAndProfilePathPrecedence(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv(queryConfigDirEnv, filepath.Join(root, "env-config"))
	t.Setenv("QUERY_PROFILES_DIR", filepath.Join(root, "env-profiles"))

	if got := ResolveConfigDir(nil); got != filepath.Join(root, "env-config") {
		t.Fatalf("env config = %q", got)
	}
	if got := ResolveConfigDir([]string{"--config-dir", filepath.Join(root, "flag-config")}); got != filepath.Join(root, "flag-config") {
		t.Fatalf("flag config = %q", got)
	}
	if got := ResolveProfilesDir(nil); got != filepath.Join(root, "env-profiles") {
		t.Fatalf("env profiles = %q", got)
	}
	if got := ResolveProfilesDir([]string{"--profiles-dir=" + filepath.Join(root, "flag-profiles")}); got != filepath.Join(root, "flag-profiles") {
		t.Fatalf("flag profiles = %q", got)
	}

	unsetEnv(t, "QUERY_PROFILES_DIR")
	if got, want := ResolveProfilesDir(nil), filepath.Join(root, "env-config", "profiles"); got != want {
		t.Fatalf("derived profiles = %q, want %q", got, want)
	}

	t.Setenv(queryDataDirEnv, filepath.Join(root, "env-data"))
	if got := resolveDataDir(filepath.Join(root, "config"), ""); got != filepath.Join(root, "env-data") {
		t.Fatalf("env data = %q", got)
	}
	if got := resolveDataDir(filepath.Join(root, "config"), filepath.Join(root, "flag-data")); got != filepath.Join(root, "flag-data") {
		t.Fatalf("flag data = %q", got)
	}
	unsetEnv(t, queryDataDirEnv)
	if got, want := resolveDataDir(filepath.Join(root, "config"), ""), filepath.Join(root, "config", "postgres"); got != want {
		t.Fatalf("derived data = %q, want %q", got, want)
	}
}
