package shell

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/flanksource/commons-db/connection"
	"github.com/joho/godotenv"
)

func buildEnv(exec Exec, resolvedEnv []string) ([]string, []string, error) {
	dotenv, err := loadDotEnv(exec.DotEnv...)
	if err != nil {
		return nil, nil, err
	}
	passthrough := hostEnv(exec.PassthroughEnv)
	return mergeEnvSlices(
		allowedHostEnv(),
		passthrough,
		envMapToSlice(dotenv),
		resolvedEnv,
	), append(environmentValues(passthrough), mapValues(dotenv)...), nil
}

func allowedHostEnv() []string {
	var envs []string
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if _, exists := allowedEnvVars[key]; exists && ok {
			envs = append(envs, item)
		}
	}
	return envs
}

func hostEnv(keys []string) []string {
	allowed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key != "" {
			allowed[key] = struct{}{}
		}
	}
	var envs []string
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if _, exists := allowed[key]; exists && ok {
			envs = append(envs, item)
		}
	}
	return envs
}

func loadDotEnv(paths ...string) (map[string]string, error) {
	merged := map[string]string{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		env, err := godotenv.Read(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read dotenv %s: %w", path, err)
		}
		for key, value := range env {
			merged[key] = value
		}
	}
	return merged, nil
}

func envMapToSlice(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, fmt.Sprintf("%s=%s", key, env[key]))
	}
	return result
}

func mergeEnvSlices(layers ...[]string) []string {
	values := map[string]string{}
	var order []string
	for _, layer := range layers {
		for _, item := range layer {
			key, value, ok := splitEnv(item)
			if !ok {
				continue
			}
			if _, seen := values[key]; !seen {
				order = append(order, key)
			}
			values[key] = value
		}
	}
	result := make([]string, 0, len(order))
	for _, key := range order {
		result = append(result, fmt.Sprintf("%s=%s", key, values[key]))
	}
	return result
}

func environmentValues(env []string) []string {
	values := make([]string, 0, len(env))
	for _, item := range env {
		_, value, ok := splitEnv(item)
		if ok {
			values = append(values, value)
		}
	}
	return values
}

func mapValues(env map[string]string) []string {
	values := make([]string, 0, len(env))
	for _, value := range env {
		values = append(values, value)
	}
	return values
}

func addedEnvironmentValues(before, after []string) []string {
	base := environmentMap(before)
	var values []string
	for key, value := range environmentMap(after) {
		if previous, exists := base[key]; !exists || previous != value {
			values = append(values, value)
		}
	}
	return values
}

func splitEnv(item string) (string, string, bool) {
	key, value, ok := strings.Cut(item, "=")
	return key, value, ok && key != ""
}

func connectionSensitiveValues(connections connection.ExecConnections) []string {
	var values []string
	if aws := connections.AWS; aws != nil {
		values = append(values, aws.AccessKey.ValueStatic, aws.SecretKey.ValueStatic, aws.SessionToken.ValueStatic)
	}
	if gcp := connections.GCP; gcp != nil && gcp.Credentials != nil {
		values = append(values, gcp.Credentials.ValueStatic)
	}
	if azure := connections.Azure; azure != nil {
		if azure.ClientID != nil {
			values = append(values, azure.ClientID.ValueStatic)
		}
		if azure.ClientSecret != nil {
			values = append(values, azure.ClientSecret.ValueStatic)
		}
	}
	if kubernetes := connections.Kubernetes; kubernetes != nil {
		if kubernetes.Kubeconfig != nil {
			values = append(values, kubernetes.Kubeconfig.ValueStatic)
		}
		if kubernetes.EKS != nil {
			values = append(values,
				kubernetes.EKS.AccessKey.ValueStatic,
				kubernetes.EKS.SecretKey.ValueStatic,
				kubernetes.EKS.SessionToken.ValueStatic,
			)
		}
		if kubernetes.GKE != nil && kubernetes.GKE.Credentials != nil {
			values = append(values, kubernetes.GKE.Credentials.ValueStatic)
		}
		if kubernetes.CNRM != nil && kubernetes.CNRM.GKE.Credentials != nil {
			values = append(values, kubernetes.CNRM.GKE.Credentials.ValueStatic)
		}
	}
	return uniqueValues(values)
}
