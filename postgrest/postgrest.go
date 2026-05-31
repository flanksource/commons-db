package postgrest

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/flanksource/clicky/exec"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons-db/api"
	"github.com/flanksource/deps"
)

func GoOffline() error {
	bin, err := install(api.DefaultConfig)
	if err != nil {
		return err
	}
	return exec.NewExec(bin, "--help").Run().Err
}

func install(config api.Config) (string, error) {
	result, err := deps.Install("postgrest", config.Postgrest.Version, deps.WithBinDir(".bin"))
	if err != nil {
		return "", err
	}
	return filepath.Join(result.BinDir, "postgrest"), nil
}

func envFor(config api.Config) map[string]string {
	return map[string]string{
		"PGRST_SERVER_PORT":              strconv.Itoa(config.Postgrest.Port),
		"PGRST_DB_URI":                   config.ConnectionString,
		"PGRST_DB_SCHEMA":                config.Schema,
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
