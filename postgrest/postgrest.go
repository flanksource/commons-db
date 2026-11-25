package postgrest

import (
	"context"
	"fmt"
	"strconv"

	"github.com/flanksource/clicky/exec"
	"github.com/flanksource/commons-db/api"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/deps/pkg/ghrelease"
)

// postgrestAssets are the PostgREST v14+ release asset names (v16 adds the
// static linux aarch64 build); ghrelease avoids linking the full deps registry.
var postgrestAssets = map[string]string{
	"darwin-amd64":  "postgrest-{{.tag}}-macos-x86-64.tar.xz",
	"darwin-arm64":  "postgrest-{{.tag}}-macos-aarch64.tar.xz",
	"linux-amd64":   "postgrest-{{.tag}}-linux-static-x86-64.tar.xz",
	"linux-arm64":   "postgrest-{{.tag}}-linux-static-aarch64.tar.xz",
	"windows-amd64": "postgrest-{{.tag}}-windows-x86-64.zip",
}

func GoOffline() error {
	bin, err := install(api.DefaultConfig)
	if err != nil {
		return err
	}
	return exec.NewExec(bin, "--help").Run().Err
}

func install(config api.Config) (string, error) {
	bin, err := ghrelease.Install(context.Background(), ghrelease.Request{
		Repo:    "PostgREST/postgrest",
		Version: config.Postgrest.Version,
		Binary:  "postgrest",
		Assets:  postgrestAssets,
		BinDir:  ".bin",
	})
	if err != nil {
		return "", fmt.Errorf("install postgrest %s: %w", config.Postgrest.Version, err)
	}
	return bin, nil
}

func envFor(config api.Config) map[string]string {
	return map[string]string{
		"PGRST_SERVER_PORT":              strconv.Itoa(config.Postgrest.Port),
		"PGRST_DB_URI":                   config.ConnectionString,
		"PGRST_DB_SCHEMAS":               config.Schema,
		"PGRST_DB_ANON_ROLE":             config.Postgrest.AnonDBRole,
		"PGRST_OPENAPI_SERVER_PROXY_URI": config.Postgrest.URL,
		"PGRST_LOG_LEVEL":                config.Postgrest.LogLevel,
		"PGRST_DB_MAX_ROWS":              strconv.Itoa(config.Postgrest.MaxRows),
		"PGRST_JWT_SECRET":               config.Postgrest.JWTSecret,
	}
}

func Start(config api.Config) {
	logger.Infof("Starting postgrest %s", config.Postgrest)
	bin, err := install(config)
	if err != nil {
		logger.Errorf("Failed to install postgREST: %v", err)
		return
	}
	if err := exec.NewExec(bin).WithEnv(envFor(config)).Start(); err != nil {
		logger.Errorf("Failed to start postgREST: %v", err)
	}
}

func PostgRESTEndpoint(config api.Config) string {
	return fmt.Sprintf("http://localhost:%d", config.Postgrest.Port)
}

func PostgRESTAdminEndpoint(config api.Config) string {
	return fmt.Sprintf("http://localhost:%d", config.Postgrest.AdminPort)
}
