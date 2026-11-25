// Package awssdk is the opt-in bridge to the AWS SDK for Go v2.
//
// The commons-db AWS clients are SDK-free; their built-in credentials chain
// covers connection keys, environment, AssumeRole, web identity (IRSA) and
// IMDSv2. Importing this package and calling Install adds the SDK's default
// chain — shared profiles, SSO and credential_process — for connections that
// carry no static keys. Config returns a full SDK config for callers that still
// drive SDK service clients themselves. This is the only package in commons-db
// that links the AWS SDK.
package awssdk

import (
	gocontext "context"
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/flanksource/commons/http/middlewares/awssigv4"
	"github.com/henvic/httpretty"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
)

// Install registers the SDK default credentials chain with the connection
// package. Call it once at startup; nothing registers it implicitly.
func Install() {
	connection.RegisterAWSCredentialsHook(resolveDefaultChain)
}

func resolveDefaultChain(ctx gocontext.Context, conn *connection.AWSConnection) (connection.AWSHookCredentials, error) {
	cfg, err := loadConfig(ctx, conn, nil)
	if err != nil {
		return connection.AWSHookCredentials{}, err
	}
	return connection.AWSHookCredentials{Provider: adaptCredentials(cfg.Credentials), Region: cfg.Region}, nil
}

// adaptCredentials exposes an SDK credentials provider as an awssigv4 provider.
func adaptCredentials(sdk aws.CredentialsProvider) awssigv4.CredentialsProvider {
	return awssigv4.CredentialsProviderFunc(func(ctx gocontext.Context) (awssigv4.Credentials, error) {
		value, err := sdk.Retrieve(ctx)
		if err != nil {
			return awssigv4.Credentials{}, err
		}
		result := awssigv4.Credentials{AccessKeyID: value.AccessKeyID, SecretAccessKey: value.SecretAccessKey, SessionToken: value.SessionToken}
		if value.CanExpire {
			result.Expires = value.Expires
		}
		return result, nil
	})
}

// Config returns an SDK config for the connection: its region, TLS setting,
// static keys (else the SDK default chain) and AssumeRole. Call it on a
// hydrated connection.
func Config(ctx context.Context, conn *connection.AWSConnection) (aws.Config, error) {
	cfg, err := loadConfig(ctx, conn, traceTransport(ctx))
	if err != nil {
		return aws.Config{}, err
	}
	if conn.AssumeRole != "" {
		cfg.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), conn.AssumeRole))
	}
	return cfg, nil
}

func loadConfig(ctx gocontext.Context, conn *connection.AWSConnection, wrap func(http.RoundTripper) http.RoundTripper) (aws.Config, error) {
	var transport http.RoundTripper = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: conn.SkipTLSVerify}}
	if wrap != nil {
		transport = wrap(transport)
	}

	options := []func(*config.LoadOptions) error{config.WithHTTPClient(&http.Client{Transport: transport})}
	if conn.Region != "" {
		options = append(options, config.WithRegion(conn.Region))
	}
	if !conn.AccessKey.IsEmpty() {
		options = append(options, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(conn.AccessKey.ValueStatic, conn.SecretKey.ValueStatic, conn.SessionToken.ValueStatic)))
	}

	cfg, err := config.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("load AWS SDK config: %w", err)
	}
	return cfg, nil
}

// traceTransport logs requests and responses when the context is tracing.
func traceTransport(ctx context.Context) func(http.RoundTripper) http.RoundTripper {
	if !ctx.IsTrace() {
		return nil
	}
	return func(rt http.RoundTripper) http.RoundTripper {
		logger := &httpretty.Logger{
			Time:           true,
			TLS:            ctx.Logger.IsLevelEnabled(7),
			RequestHeader:  true,
			RequestBody:    ctx.Logger.IsLevelEnabled(8),
			ResponseHeader: true,
			ResponseBody:   ctx.Logger.IsLevelEnabled(9),
			Colors:         true,
			Formatters:     []httpretty.Formatter{&httpretty.JSONFormatter{}},
		}
		return logger.RoundTripper(rt)
	}
}
