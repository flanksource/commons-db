package connection

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	osExec "os/exec"
	"path/filepath"
	"strings"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons/logger"
	"github.com/samber/lo"

	textTemplate "text/template"
)

// kubeEnvVars holds a list of environment variables that are commonly used
// to configure access to the default Kubernetes cluster
var kubeEnvVars = []string{
	"KUBECONFIG",
	"KUBERNETES_SERVICE_HOST",
	"KUBERNETES_SERVICE_PORT",
	"KUBERNETES_PORT_443_TCP",
	"KUBERNETES_SERVICE_PORT_HTTPS",
	"KUBERNETES_PORT_443_TCP_PROTO",
	"KUBERNETES_PORT_443_TCP_ADDR",
	"KUBERNETES_PORT",
	"KUBERNETES_PORT_443_TCP_PORT",
}

// +kubebuilder:object:generate=true
type ExecConnections struct {
	FromConfigItem *string `yaml:"fromConfigItem,omitempty" json:"fromConfigItem,omitempty" template:"true"`

	// EKSPodIdentity when enabled will allow access to AWS_* env vars
	EKSPodIdentity bool `json:"eksPodIdentity,omitempty"`

	// ServiceAccount when enabled will allow access to KUBERNETES env vars
	ServiceAccount bool `json:"serviceAccount,omitempty"`

	Kubernetes *KubernetesConnection `yaml:"kubernetes,omitempty" json:"kubernetes,omitempty"`
	AWS        *AWSConnection        `yaml:"aws,omitempty" json:"aws,omitempty"`
	GCP        *GCPConnection        `yaml:"gcp,omitempty" json:"gcp,omitempty"`
	Azure      *AzureConnection      `yaml:"azure,omitempty" json:"azure,omitempty"`
	OpenSearch *OpenSearchConnection `yaml:"opensearch,omitempty" json:"opensearch,omitempty"`
}

func saveConfig(cwd string, configTemplate *textTemplate.Template, view any) (configPath string, resultErr error) {
	dirPath := filepath.Join(cwd, ".creds", fmt.Sprintf("cred-%d", rand.Intn(10000000)))
	if err := os.MkdirAll(dirPath, 0700); err != nil {
		return "", fmt.Errorf("create credentials directory: %w", pathErrorCause(err))
	}
	defer func() {
		if resultErr == nil {
			return
		}
		if cleanupErr := os.RemoveAll(dirPath); cleanupErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("failed to cleanup partial credentials"))
		}
	}()

	configPath = fmt.Sprintf("%s/credentials", dirPath)
	logger.Tracef("Creating temporary credentials file")

	file, err := os.Create(configPath)
	if err != nil {
		return configPath, fmt.Errorf("create credentials file: %w", pathErrorCause(err))
	}

	if err := configTemplate.Execute(file, view); err != nil {
		_ = file.Close()
		return configPath, err
	}
	if err := file.Close(); err != nil {
		return configPath, fmt.Errorf("close credentials file: %w", pathErrorCause(err))
	}

	return configPath, nil
}

func pathErrorCause(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

var (
	awsConfigTemplate        *textTemplate.Template
	kubernetesConfigTemplate *textTemplate.Template
	gcloudConfigTemplate     *textTemplate.Template
)

func init() {
	awsConfigTemplate = textTemplate.Must(textTemplate.New("").Parse(`[default]
aws_access_key_id = {{.AccessKey.ValueStatic}}
aws_secret_access_key = {{.SecretKey.ValueStatic}}
{{if .SessionToken.ValueStatic}}aws_session_token={{.SessionToken.ValueStatic}}{{end}}
`))

	gcloudConfigTemplate = textTemplate.Must(textTemplate.New("").Parse(`{{.Credentials}}`))

	kubernetesConfigTemplate = textTemplate.Must(textTemplate.New("").Parse(`{{.Kubeconfig.ValueStatic}}`))
}

type ConnectionSetupResult struct {
	Sources   []string `json:"source,omitempty"`
	EnvVars   []string `json:"envVars,omitempty"`
	ApiServer string   `json:"kubeApiServer,omitempty"`

	Cleanup func() error `json:"-"`
}

func injectEksPodIdentity(ctx context.Context, cmd *osExec.Cmd) {
	ctx.Logger.V(3).Infof("Injecting EKS Pod Identity")

	for _, env := range os.Environ() {
		key, _, ok := strings.Cut(env, "=")
		if !ok {
			continue
		}

		if strings.HasPrefix(key, "AWS_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
}

func injectKubernetesServiceAccount(ctx context.Context, cmd *osExec.Cmd) {
	ctx.Logger.V(3).Infof("Injecting Kubernetes service account")
	for _, env := range os.Environ() {
		key, _, ok := strings.Cut(env, "=")
		if !ok {
			continue
		}
		if lo.Contains(kubeEnvVars, key) {
			cmd.Env = append(cmd.Env, env)
		}
	}
}

// SetupConnections creates the necessary credential files and injects env vars into the cmd
func SetupConnection(ctx context.Context, connections ExecConnections, cmd *osExec.Cmd) (result *ConnectionSetupResult, resultErr error) {
	var output ConnectionSetupResult
	var cleaners []func() error
	cleanup := func() error { return cleanupConnectionFiles(cleaners) }
	defer func() {
		if resultErr == nil {
			return
		}
		if cleanupErr := cleanup(); cleanupErr != nil {
			resultErr = errors.Join(resultErr, cleanupErr)
		}
	}()

	if lo.FromPtr(connections.FromConfigItem) != "" {
		return nil, fmt.Errorf("connection.fromConfigItem is not supported in commons-db (scraper configs are mission-control specific)")
	}

	if connections.Kubernetes != nil {
		// The kubernetes connection didn't come from `fromConfigItem`, so we hydrate it here
		ctx = ctx.WithKubernetes(connections.Kubernetes)
		cleaner, err := setupKubernetesConnection(ctx, connections.Kubernetes, cmd, &output)
		if cleaner != nil {
			cleaners = append(cleaners, cleaner)
		}
		if err != nil {
			return nil, err
		}
	}

	if connections.ServiceAccount {
		injectKubernetesServiceAccount(ctx, cmd)
	}

	if connections.AWS != nil {
		cleaner, err := setupAWSConnection(ctx, connections.AWS, cmd, &output)
		if err != nil {
			return nil, err
		}
		cleaners = append(cleaners, cleaner)
	} else if connections.EKSPodIdentity {
		injectEksPodIdentity(ctx, cmd)
	}

	if connections.Azure != nil {
		cleaner, err := setupAzureConnection(ctx, connections.Azure, cmd, &output)
		if cleaner != nil {
			cleaners = append(cleaners, cleaner)
		}
		if err != nil {
			return nil, err
		}
	}

	if connections.GCP != nil {
		cleaner, err := setupGCPConnection(ctx, connections.GCP, cmd, &output)
		if cleaner != nil {
			cleaners = append(cleaners, cleaner)
		}
		if err != nil {
			return nil, err
		}
	}
	if connections.OpenSearch != nil {
		if err := setupOpenSearchConnection(ctx, connections.OpenSearch, cmd); err != nil {
			return nil, err
		}
	}

	output.EnvVars = cmd.Env

	output.Cleanup = cleanup

	return &output, nil
}

func removeConfigDir(configPath string) func() error {
	return func() error {
		return os.RemoveAll(filepath.Dir(configPath))
	}
}

func setupKubernetesConnection(ctx context.Context, conn *KubernetesConnection, cmd *osExec.Cmd, output *ConnectionSetupResult) (func() error, error) {
	if _, _, err := conn.Populate(ctx, true); err != nil {
		return nil, fmt.Errorf("failed to hydrate kubernetes connection: %w", err)
	}

	if _, pathErr := os.Stat(conn.Kubeconfig.ValueStatic); pathErr == nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", conn.Kubeconfig.ValueStatic))
		f, err := os.ReadFile(conn.Kubeconfig.ValueStatic)
		if err != nil {
			return nil, fmt.Errorf("failed to read kubeconfig: %w", err)
		}
		apiServer, err := kubernetes.GetAPIServer(f)
		if err != nil {
			return nil, fmt.Errorf("failed to get api server: %w", err)
		}
		output.ApiServer = apiServer
		return nil, nil
	}

	configPath, err := saveConfig(cmd.Dir, kubernetesConfigTemplate, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to store kubernetes credentials: %w", err)
	}
	cleaner := removeConfigDir(configPath)

	apiServer, err := kubernetes.GetAPIServer([]byte(conn.Kubeconfig.ValueStatic))
	if err != nil {
		return cleaner, fmt.Errorf("failed to get api server: %w", err)
	}
	output.ApiServer = apiServer

	cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", configPath))
	return cleaner, nil
}

func setupAWSConnection(ctx context.Context, conn *AWSConnection, cmd *osExec.Cmd, output *ConnectionSetupResult) (func() error, error) {
	if err := conn.Populate(ctx); err != nil {
		return nil, fmt.Errorf("failed to hydrate aws connection: %w", err)
	}

	output.Sources = append(output.Sources, fmt.Sprintf("awsConnection: %s", conn.ConnectionName))
	configPath, err := saveConfig(cmd.Dir, awsConfigTemplate, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to store AWS credentials: %w", err)
	}

	cmd.Env = append(cmd.Env, "AWS_EC2_METADATA_DISABLED=true") // https://github.com/aws/aws-cli/issues/5262#issuecomment-705832151
	cmd.Env = append(cmd.Env, fmt.Sprintf("AWS_SHARED_CREDENTIALS_FILE=%s", configPath))
	if conn.Region != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("AWS_DEFAULT_REGION=%s", conn.Region))
	}
	return removeConfigDir(configPath), nil
}

func setupAzureConnection(ctx context.Context, conn *AzureConnection, cmd *osExec.Cmd, output *ConnectionSetupResult) (func() error, error) {
	if err := conn.HydrateConnection(ctx); err != nil {
		return nil, fmt.Errorf("failed to hydrate connection %w", err)
	}
	if conn.ClientID == nil || conn.ClientID.IsEmpty() || conn.ClientSecret == nil || conn.ClientSecret.IsEmpty() || conn.TenantID == "" {
		return nil, fmt.Errorf("azure CLI login requires clientID, clientSecret and tenantID")
	}
	cleaner, err := isolateCloudConfig(cmd, "AZURE_CONFIG_DIR")
	if err != nil {
		return nil, err
	}
	output.Sources = append(output.Sources, fmt.Sprintf("azureConnection: %s", conn.ConnectionName))

	// login with service principal
	runCmd := osExec.CommandContext(ctx, "az", "login", "--service-principal", "--username", conn.ClientID.ValueStatic, "--password", conn.ClientSecret.ValueStatic, "--tenant", conn.TenantID)
	runCmd.Env = cmd.Env
	if err := runCmd.Run(); err != nil {
		return cleaner, fmt.Errorf("failed to login: %w", err)
	}
	return cleaner, nil
}

func setupGCPConnection(ctx context.Context, conn *GCPConnection, cmd *osExec.Cmd, output *ConnectionSetupResult) (func() error, error) {
	if err := conn.HydrateConnection(ctx); err != nil {
		return nil, fmt.Errorf("failed to hydrate connection %w", err)
	}
	if conn.Credentials == nil || conn.Credentials.IsEmpty() {
		return nil, fmt.Errorf("GCP CLI login requires credentials")
	}

	output.Sources = append(output.Sources, fmt.Sprintf("gcpConnection: %s", conn.ConnectionName))

	configPath, err := saveConfig(cmd.Dir, gcloudConfigTemplate, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to store gcloud credentials: %w", err)
	}
	cleaner := removeConfigDir(configPath)
	cacheCleanup, err := isolateCloudConfig(cmd, "CLOUDSDK_CONFIG")
	if err != nil {
		return cleaner, err
	}
	cleanup := func() error { return errors.Join(cleaner(), cacheCleanup()) }

	// to configure gcloud CLI to use the service account specified in GOOGLE_APPLICATION_CREDENTIALS,
	// we need to explicitly activate it
	runCmd := osExec.CommandContext(ctx, "gcloud", "auth", "activate-service-account", "--key-file", configPath)
	runCmd.Env = cmd.Env
	if err := runCmd.Run(); err != nil {
		return cleanup, fmt.Errorf("failed to activate GCP service account: %w", err)
	}

	cmd.Env = append(cmd.Env, fmt.Sprintf("GOOGLE_APPLICATION_CREDENTIALS=%s", configPath))
	return cleanup, nil
}

func isolateCloudConfig(cmd *osExec.Cmd, variable string) (func() error, error) {
	base := filepath.Join(cmd.Dir, ".creds")
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(base, strings.ToLower(variable)+"-")
	if err != nil {
		return nil, err
	}
	for i := len(cmd.Env) - 1; i >= 0; i-- {
		if strings.HasPrefix(cmd.Env[i], variable+"=") {
			cmd.Env = append(cmd.Env[:i], cmd.Env[i+1:]...)
		}
	}
	cmd.Env = append(cmd.Env, variable+"="+dir)
	return func() error { return os.RemoveAll(dir) }, nil
}

func cleanupConnectionFiles(cleaners []func() error) error {
	failures := 0
	for _, cleanup := range cleaners {
		if err := cleanup(); err != nil {
			failures++
		}
	}
	if failures > 0 {
		return fmt.Errorf("failed to cleanup %d credential locations", failures)
	}
	return nil
}
