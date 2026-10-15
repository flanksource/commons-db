package connection

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/storage/memory"
)

// ProbeResult is the read-only capability discovered by a connection test.
type ProbeResult struct {
	OK bool `json:"ok"`
}

// ProbeModel checks the service or repository represented by a persisted
// connection. Credentials are resolved only for this call and never returned.
func ProbeModel(ctx context.Context, model models.Connection) (ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resolved, err := resolveProbeModel(dbcontext.NewContext(ctx), model)
	if err != nil {
		return ProbeResult{}, err
	}
	switch resolved.Type {
	case models.ConnectionTypePostgres, models.ConnectionTypeMySQL, models.ConnectionTypeSQLServer, models.ConnectionTypeSQLite:
		return probeSQL(ctx, resolved)
	case models.ConnectionTypeGit:
		return probeGit(dbcontext.NewContext(ctx), resolved)
	case models.ConnectionTypeGithub, models.ConnectionTypeGitlab, models.ConnectionTypeAzureDevops:
		return probeAccount(ctx, resolved)
	case models.ConnectionTypeS3:
		return probeS3(dbcontext.NewContext(ctx), resolved)
	default:
		return ProbeResult{}, fmt.Errorf("unsupported connection type %q", resolved.Type)
	}
}

func resolveProbeModel(ctx dbcontext.Context, model models.Connection) (models.Connection, error) {
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"url", &model.URL}, {"username", &model.Username}, {"password", &model.Password}, {"certificate", &model.Certificate},
	} {
		var value types.EnvVar
		if err := value.Scan(*field.value); err != nil {
			return models.Connection{}, fmt.Errorf("parse %s reference: %w", field.name, err)
		}
		resolved, err := ctx.GetEnvValueFromCache(value, model.Namespace)
		if err != nil {
			return models.Connection{}, fmt.Errorf("resolve %s reference: %w", field.name, err)
		}
		*field.value = resolved
	}
	return model, nil
}

func probeSQL(ctx context.Context, model models.Connection) (ProbeResult, error) {
	var connection SQLConnection
	if err := connection.FromModel(model); err != nil {
		return ProbeResult{}, err
	}
	connection.ConnectionName = ""
	client, err := connection.Client(dbcontext.NewContext(ctx))
	if err != nil {
		return ProbeResult{}, err
	}
	defer client.Close()
	if err := client.PingContext(ctx); err != nil {
		return ProbeResult{}, fmt.Errorf("%s ping failed: %w", model.Type, err)
	}
	return ProbeResult{OK: true}, nil
}

func probeGit(ctx dbcontext.Context, model models.Connection) (ProbeResult, error) {
	connection := GitConnection{
		URL: model.URL, Username: &types.EnvVar{ValueStatic: model.Username},
		Password: &types.EnvVar{ValueStatic: model.Password}, Certificate: &types.EnvVar{ValueStatic: model.Certificate},
	}
	client, err := CreateGitConfig(ctx, &connection)
	if err != nil {
		return ProbeResult{}, err
	}
	remote := git.NewRemote(memory.NewStorage(), &gitconfig.RemoteConfig{Name: "origin", URLs: []string{client.URL}})
	restore := configureGitHTTPTransport(ctx, client.URL, client.TLS)
	defer restore()
	if _, err := remote.ListContext(ctx, &git.ListOptions{Auth: client.Auth}); err != nil {
		return ProbeResult{}, fmt.Errorf("git remote test failed: %w", err)
	}
	return ProbeResult{OK: true}, nil
}

func probeAccount(ctx context.Context, model models.Connection) (ProbeResult, error) {
	base := model.URL
	if base == "" && model.Type == models.ConnectionTypeGithub {
		base = "https://api.github.com"
	}
	if base == "" {
		return ProbeResult{}, fmt.Errorf("%s API URL is required", model.Type)
	}
	endpoint, err := url.Parse(base)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
		return ProbeResult{}, fmt.Errorf("%s API URL must be an HTTPS URL", model.Type)
	}
	if strings.TrimSpace(model.Password) == "" {
		return ProbeResult{}, fmt.Errorf("%s access token is required", model.Type)
	}
	switch model.Type {
	case models.ConnectionTypeGithub:
		endpoint.Path = path.Join(endpoint.Path, "user")
	case models.ConnectionTypeGitlab:
		endpoint.Path = path.Join(endpoint.Path, "api/v4/user")
	case models.ConnectionTypeAzureDevops:
		endpoint.Path = path.Join(endpoint.Path, "_apis/projects")
		query := endpoint.Query()
		query.Set("api-version", "7.1")
		endpoint.RawQuery = query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return ProbeResult{}, err
	}
	if model.Type == models.ConnectionTypeAzureDevops {
		request.SetBasicAuth("", model.Password)
	} else {
		request.Header.Set("Authorization", "Bearer "+model.Password)
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(next *http.Request, _ []*http.Request) error {
		if next.URL.Scheme != "https" {
			return fmt.Errorf("%s API redirected to a non-HTTPS URL", model.Type)
		}
		return nil
	}}
	if model.InsecureTLS {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	response, err := client.Do(request)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("%s API request failed: %w", model.Type, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ProbeResult{}, fmt.Errorf("%s API returned HTTP %d", model.Type, response.StatusCode)
	}
	return ProbeResult{OK: true}, nil
}

func probeS3(ctx dbcontext.Context, model models.Connection) (ProbeResult, error) {
	var connection AWSConnection
	connection.FromModel(model)
	connection.ConnectionName = ""
	if err := connection.Populate(ctx); err != nil {
		return ProbeResult{}, err
	}
	config, err := connection.Client(ctx)
	if err != nil {
		return ProbeResult{}, err
	}
	client := s3.NewFromConfig(config, func(options *s3.Options) {
		if connection.Endpoint != "" {
			options.BaseEndpoint = aws.String(connection.Endpoint)
		}
		options.UsePathStyle = model.Properties["usePathStyle"] == "true"
	})
	if bucket := strings.TrimSpace(model.Properties["bucket"]); bucket != "" {
		if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)}); err != nil {
			return ProbeResult{}, fmt.Errorf("s3 head bucket %q failed: %w", bucket, err)
		}
	} else if _, err := client.ListBuckets(ctx, &s3.ListBucketsInput{}); err != nil {
		return ProbeResult{}, fmt.Errorf("s3 list buckets failed (requires ListAllMyBuckets permission): %w", err)
	}
	return ProbeResult{OK: true}, nil
}
