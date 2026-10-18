package connection

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	dbcontext "github.com/flanksource/commons-db/context"
	dbgit "github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
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
	case models.ConnectionTypeGit, models.ConnectionTypeGithub, models.ConnectionTypeGitlab, models.ConnectionTypeAzureDevops:
		return probeGitProvider(dbcontext.NewContext(ctx), resolved)
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

// probeGitProvider verifies a git or git-provider connection through its
// provider: ls-remote for plain git, the authenticated user/projects API for
// GitHub, GitLab and Azure DevOps.
func probeGitProvider(ctx dbcontext.Context, model models.Connection) (ProbeResult, error) {
	client, err := dbgit.FromConnection(ctx, model)
	if err != nil {
		return ProbeResult{}, err
	}
	if err := client.Provider.Verify(ctx); err != nil {
		return ProbeResult{}, fmt.Errorf("%s connection test failed: %w", model.Type, err)
	}
	return ProbeResult{OK: true}, nil
}

func probeS3(ctx dbcontext.Context, model models.Connection) (ProbeResult, error) {
	usePathStyle := false
	if raw := model.Properties["usePathStyle"]; raw != "" {
		var err error
		usePathStyle, err = strconv.ParseBool(raw)
		if err != nil {
			return ProbeResult{}, fmt.Errorf("invalid S3 usePathStyle %q: %w", raw, err)
		}
	}
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
		options.UsePathStyle = usePathStyle
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
