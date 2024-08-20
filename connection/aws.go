package connection

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/flanksource/duty/context"
	"github.com/flanksource/duty/types"
	"github.com/henvic/httpretty"
)

// +kubebuilder:object:generate=true
type AWSConnection struct {
	// ConnectionName of the connection. It'll be used to populate the endpoint, accessKey and secretKey.
	ConnectionName string       `yaml:"connection,omitempty" json:"connection,omitempty"`
	AccessKey      types.EnvVar `yaml:"accessKey" json:"accessKey,omitempty"`
	SecretKey      types.EnvVar `yaml:"secretKey" json:"secretKey,omitempty"`
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

// Populate populates an AWSConnection with credentials and other information.
// If a connection name is specified, it'll be used to populate the endpoint, accessKey and secretKey.
func (t *AWSConnection) Populate(ctx ConnectionContext) error {
	if t.ConnectionName != "" {
		connection, err := ctx.HydrateConnectionByURL(t.ConnectionName)
		if err != nil {
			return fmt.Errorf("could not parse EC2 access key: %v", err)
		}

		t.AccessKey.ValueStatic = connection.Username
		t.SecretKey.ValueStatic = connection.Password
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
		return fmt.Errorf("could not parse AWS access key id: %v", err)
	} else {
		t.AccessKey.ValueStatic = accessKey
	}

	if secretKey, err := ctx.GetEnvValueFromCache(t.SecretKey, ctx.GetNamespace()); err != nil {
		return fmt.Errorf("could not parse AWS secret access key: %w", err)
	} else {
		t.SecretKey.ValueStatic = secretKey
	}

	if sessionToken, err := ctx.GetEnvValueFromCache(t.SessionToken, ctx.GetNamespace()); err != nil {
		return fmt.Errorf("could not parse AWS session token: %w", err)
	} else {
		t.SessionToken.ValueStatic = sessionToken
	}

	return nil
}

// Client returns a new aws config.
// Call this on a hydrated connection.
func (t *AWSConnection) Client(ctx context.Context) (aws.Config, error) {
	var tr http.RoundTripper
	tr = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: t.SkipTLSVerify},
	}

	if ctx.IsTrace() {
		httplogger := &httpretty.Logger{
			Time:           true,
			TLS:            true,
			RequestHeader:  true,
			RequestBody:    true,
			ResponseHeader: true,
			ResponseBody:   true,
			Colors:         true,
			Formatters:     []httpretty.Formatter{&httpretty.JSONFormatter{}},
		}

		tr = httplogger.RoundTripper(tr)
	}

	options := []func(*config.LoadOptions) error{
		config.WithRegion(t.Region),
		config.WithHTTPClient(&http.Client{Transport: tr}),
	}

	if !t.AccessKey.IsEmpty() {
		options = append(options, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(t.AccessKey.ValueStatic, t.SecretKey.ValueStatic, "")))
	}

	cfg, err := config.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return aws.Config{}, err
	}

	if t.AssumeRole != "" {
		cfg.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), t.AssumeRole))
	}

	return cfg, nil
}
