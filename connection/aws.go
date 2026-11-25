package connection

import (
	"fmt"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	commonshttp "github.com/flanksource/commons/http"
	"github.com/flanksource/commons/logger"
)

// +kubebuilder:object:generate=true
type AWSConnection struct {
	// ConnectionName of the connection. It'll be used to populate the endpoint, accessKey and secretKey.
	ConnectionName string       `yaml:"connection,omitempty" json:"connection,omitempty"`
	AccessKey      types.EnvVar `yaml:"accessKey,omitempty" json:"accessKey,omitempty"`
	SecretKey      types.EnvVar `yaml:"secretKey,omitempty" json:"secretKey,omitempty"`
	SessionToken   types.EnvVar `yaml:"sessionToken,omitempty" json:"sessionToken,omitempty"`
	AssumeRole     string       `yaml:"assumeRole,omitempty" json:"assumeRole,omitempty"`
	Region         string       `yaml:"region,omitempty" json:"region,omitempty"`
	Endpoint       string       `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	// Skip TLS verify when connecting to aws
	SkipTLSVerify bool `yaml:"skipTLSVerify,omitempty" json:"skipTLSVerify,omitempty"`
}

func (t *AWSConnection) GetUsername() types.EnvVar {
	return t.AccessKey
}

func (t *AWSConnection) GetPassword() types.EnvVar {
	return t.SecretKey
}

func (t *AWSConnection) GetProperties() map[string]string {
	return map[string]string{
		"region": t.Region,
	}
}

func (t *AWSConnection) GetURL() types.EnvVar {
	return types.EnvVar{ValueStatic: t.Endpoint}
}

func (t *AWSConnection) FromModel(connection models.Connection) {
	t.ConnectionName = connection.Name
	t.AccessKey = types.EnvVar{ValueStatic: connection.Username}
	t.SecretKey = types.EnvVar{ValueStatic: connection.Password}
	t.Endpoint = connection.URL
	t.SkipTLSVerify = connection.InsecureTLS
	if region, ok := connection.Properties["region"]; ok {
		t.Region = region
	}
	if assumeRole, ok := connection.Properties["assumeRole"]; ok {
		t.AssumeRole = assumeRole
	}
}

func (t AWSConnection) ToModel() models.Connection {
	return models.Connection{
		Type:        models.ConnectionTypeAWS,
		Username:    t.AccessKey.ValueStatic,
		Password:    t.SecretKey.ValueStatic,
		URL:         t.Endpoint,
		InsecureTLS: t.SkipTLSVerify,
		Properties: types.JSONStringMap{
			"region":     t.Region,
			"assumeRole": t.AssumeRole,
		},
	}
}

// Populate populates an AWSConnection with credentials and other information.
// If a connection name is specified, it'll be used to populate the endpoint, accessKey and secretKey.
func (t *AWSConnection) Populate(ctx ConnectionContext) error {
	if t.ConnectionName != "" {
		connection, err := ctx.HydrateConnectionByURL(t.ConnectionName)
		if err != nil {
			return fmt.Errorf("could not parse EC2 access key: %w", err)
		}
		if connection == nil {
			return fmt.Errorf("connection[%s] not found", t.ConnectionName)
		}

		t.AccessKey.ValueStatic = connection.Username
		t.SecretKey.ValueStatic = connection.Password
		if t.SessionToken.IsEmpty() {
			t.SessionToken.ValueStatic = connection.Properties["sessionToken"]
		}
		if t.Endpoint == "" {
			t.Endpoint = connection.URL
		}

		t.SkipTLSVerify = connection.InsecureTLS
		if t.Region == "" {
			if region, ok := connection.Properties["region"]; ok {
				t.Region = region
			}
		}

		if t.AssumeRole == "" {
			if role, ok := connection.Properties["assumeRole"]; ok {
				t.AssumeRole = role
			}
		}
	}

	if accessKey, err := ctx.GetEnvValueFromCache(t.AccessKey, ctx.GetNamespace()); err != nil {
		return fmt.Errorf("could not get AWS access key id from env var: %w", err)
	} else {
		t.AccessKey.ValueStatic = accessKey
	}

	if secretKey, err := ctx.GetEnvValueFromCache(t.SecretKey, ctx.GetNamespace()); err != nil {
		return fmt.Errorf("could not get AWS secret access key from env var: %w", err)
	} else {
		t.SecretKey.ValueStatic = secretKey
	}

	if sessionToken, err := ctx.GetEnvValueFromCache(t.SessionToken, ctx.GetNamespace()); err != nil {
		return fmt.Errorf("could not get AWS session token from env var: %w", err)
	} else {
		t.SessionToken.ValueStatic = sessionToken
	}

	return nil
}

// Client returns the SDK-free AWS session for the connection: region, endpoint,
// TLS settings and the credentials chain described in aws_credentials.go.
// Call this on a hydrated connection.
func (t *AWSConnection) Client(ctx context.Context) (*cloudaws.Session, error) {
	credentials, err := t.resolveCredentials(ctx)
	if err != nil {
		return nil, err
	}
	return cloudaws.NewSession(cloudaws.SessionOptions{
		Region:        credentials.Region,
		Endpoint:      t.Endpoint,
		Credentials:   credentials.Provider,
		SkipTLSVerify: t.SkipTLSVerify,
		Customize:     traceHTTP(ctx),
	})
}

// traceHTTP adds request/response logging to each service client when the
// context is tracing.
func traceHTTP(ctx context.Context) func(*commonshttp.Client) *commonshttp.Client {
	if !ctx.IsTrace() {
		return nil
	}
	return func(c *commonshttp.Client) *commonshttp.Client {
		return c.WithLogger(ctx.Logger).WithHttpLogging(logger.Trace, logger.Trace2)
	}
}
