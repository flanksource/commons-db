package connection

import (
	gocontext "context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/flanksource/commons/http/middlewares/awssigv4"
)

// AWSHookCredentials is what an AWSCredentialsHook resolves: the signing
// credentials and, optionally, the region its configuration knows about.
type AWSHookCredentials struct {
	Provider awssigv4.CredentialsProvider
	// Region is used only when neither the connection nor the environment names one.
	Region string
}

// AWSCredentialsHook supplies credentials for a connection that carries no
// static access key, in place of the built-in chain. The opt-in
// connection/awssdk package registers one that adapts the AWS SDK's default
// chain (shared profiles, SSO, credential_process).
type AWSCredentialsHook func(ctx gocontext.Context, conn *AWSConnection) (AWSHookCredentials, error)

var awsCredentialsHook atomic.Pointer[AWSCredentialsHook]

// RegisterAWSCredentialsHook installs the hook for the process; nil removes it.
// It is explicit by design: nothing registers a hook from an init function.
func RegisterAWSCredentialsHook(hook AWSCredentialsHook) {
	if hook == nil {
		awsCredentialsHook.Store(nil)
		return
	}
	awsCredentialsHook.Store(&hook)
}

const builtInChainSources = "connection static access key (absent), environment (AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY), " +
	"web identity (AWS_WEB_IDENTITY_TOKEN_FILE/AWS_ROLE_ARN), container credentials (AWS_CONTAINER_CREDENTIALS_RELATIVE_URI/AWS_CONTAINER_CREDENTIALS_FULL_URI), " +
	"EC2 instance metadata (IMDSv2)"

// resolveCredentials builds the signing credentials and region for the connection.
//
// The base credentials are the connection's static keys, else the registered
// hook, else the built-in chain of environment, web identity (IRSA), container credentials (ECS/EKS) and IMDSv2.
// AssumeRole, when configured, is layered on top of the base.
func (t *AWSConnection) resolveCredentials(ctx gocontext.Context) (AWSHookCredentials, error) {
	region := firstNonEmptyString(t.Region, os.Getenv("AWS_REGION"), os.Getenv("AWS_DEFAULT_REGION"))
	base, err := t.baseCredentials(ctx, region)
	if err != nil {
		return AWSHookCredentials{}, err
	}
	region = firstNonEmptyString(region, base.Region)
	if region == "" {
		return AWSHookCredentials{}, fmt.Errorf("aws connection has no region: set region on the connection or AWS_REGION")
	}
	provider := base.Provider
	if t.AssumeRole != "" {
		provider = awssigv4.NewAssumeRoleProvider(t.assumeRoleOptions(region, provider))
	}
	return AWSHookCredentials{Provider: awssigv4.NewCredentialsCache(provider), Region: region}, nil
}

// assumeRoleOptions configures the STS AssumeRole call. It deliberately sets no
// Endpoint: the connection's Endpoint override is for S3, SQS and CloudWatch
// only, and STS always uses the AWS endpoint.
func (t *AWSConnection) assumeRoleOptions(region string, base awssigv4.CredentialsProvider) awssigv4.AssumeRoleOptions {
	return awssigv4.AssumeRoleOptions{
		Region:      region,
		HTTPClient:  t.providerHTTPClient(),
		Base:        base,
		RoleARN:     t.AssumeRole,
		SessionName: fmt.Sprintf("commons-db-%d", time.Now().UnixNano()),
	}
}

func (t *AWSConnection) baseCredentials(ctx gocontext.Context, region string) (AWSHookCredentials, error) {
	if !t.AccessKey.IsEmpty() {
		return AWSHookCredentials{Provider: awssigv4.NewStaticCredentialsProvider(awssigv4.Credentials{
			AccessKeyID:     t.AccessKey.ValueStatic,
			SecretAccessKey: t.SecretKey.ValueStatic,
			SessionToken:    t.SessionToken.ValueStatic,
		})}, nil
	}
	if hook := awsCredentialsHook.Load(); hook != nil {
		resolved, err := (*hook)(ctx, t)
		if err != nil {
			return AWSHookCredentials{}, fmt.Errorf("aws sdk credentials hook: %w", err)
		}
		if resolved.Provider == nil {
			return AWSHookCredentials{}, fmt.Errorf("aws sdk credentials hook returned no provider")
		}
		return resolved, nil
	}
	return AWSHookCredentials{Provider: builtInChain{awssigv4.DefaultChain(region)}}, nil
}

// providerHTTPClient is the HTTP client for STS calls made by credential providers.
func (t *AWSConnection) providerHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: t.SkipTLSVerify}},
	}
}

// builtInChain labels a failed chain with the sources it tried and where to get
// the remaining ones.
type builtInChain struct{ awssigv4.CredentialsProvider }

func (c builtInChain) Retrieve(ctx gocontext.Context) (awssigv4.Credentials, error) {
	credentials, err := c.CredentialsProvider.Retrieve(ctx)
	if err != nil {
		return awssigv4.Credentials{}, fmt.Errorf("no AWS credentials resolved; tried %s. "+
			"Profile, SSO and credential_process chains require importing github.com/flanksource/commons-db/connection/awssdk "+
			"and calling awssdk.Install(): %w", builtInChainSources, err)
	}
	return credentials, nil
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
