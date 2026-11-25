// Package aws is a thin, SDK-free client for the AWS APIs commons-db uses.
//
// A Session carries the region, an optional endpoint override (emulators, VPC
// endpoints), a SigV4 credentials provider and the commons HTTP client
// configuration. Typed service clients hang off it and expose only the
// operations and fields the callers read.
package aws

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	commonshttp "github.com/flanksource/commons/http"
	"github.com/flanksource/commons/http/middlewares/awssigv4"
)

// Service names, as used by SessionOptions.ServiceEndpoints.
const (
	ServiceS3         = "s3"
	ServiceSQS        = "sqs"
	ServiceCloudWatch = "monitoring"
	ServiceLogs       = "logs"
	ServiceSTS        = "sts"
	ServiceKMS        = "kms"
	ServiceEKS        = "eks"
)

// endpointScoped are the services SessionOptions.Endpoint applies to: the ones
// the AWS SDK clients were given a BaseEndpoint for. STS, KMS and EKS always use
// the AWS endpoints unless named in ServiceEndpoints.
var endpointScoped = map[string]bool{ServiceS3: true, ServiceSQS: true, ServiceCloudWatch: true, ServiceLogs: true}

var knownServices = map[string]bool{
	ServiceS3: true, ServiceSQS: true, ServiceCloudWatch: true, ServiceLogs: true,
	ServiceSTS: true, ServiceKMS: true, ServiceEKS: true,
}

// SessionOptions configures a Session.
type SessionOptions struct {
	// Region is the signing region and, without an Endpoint, the endpoint region.
	Region string
	// Endpoint overrides the S3, SQS, CloudWatch and CloudWatch Logs endpoints,
	// for emulators and VPC endpoints. It is an http(s) URL with no path. S3
	// addresses buckets as subdomains of it unless S3Options.PathStyle is set.
	Endpoint string
	// ServiceEndpoints overrides individual services by name (the Service*
	// constants), for emulators that also serve STS, KMS or EKS. Entries take
	// precedence over Endpoint.
	ServiceEndpoints map[string]string
	// Credentials signs every request.
	Credentials awssigv4.CredentialsProvider
	// SkipTLSVerify disables certificate verification for HTTPS endpoints.
	SkipTLSVerify bool
	// Customize adjusts each service's HTTP client, e.g. to add request logging.
	Customize func(*commonshttp.Client) *commonshttp.Client
}

// Session is the shared configuration behind every typed service client.
type Session struct {
	options  SessionOptions
	endpoint string
	// serviceEndpoints are the normalized ServiceEndpoints.
	serviceEndpoints map[string]string
}

// NewSession validates options. Region and Credentials are required.
func NewSession(options SessionOptions) (*Session, error) {
	if options.Region == "" {
		return nil, fmt.Errorf("aws session requires a region")
	}
	if options.Credentials == nil {
		return nil, fmt.Errorf("aws session requires a credentials provider")
	}
	endpoint, err := normalizeEndpoint(options.Endpoint)
	if err != nil {
		return nil, err
	}
	serviceEndpoints := make(map[string]string, len(options.ServiceEndpoints))
	for name, raw := range options.ServiceEndpoints {
		if !knownServices[name] {
			return nil, fmt.Errorf("aws session: unknown service %q in ServiceEndpoints", name)
		}
		if serviceEndpoints[name], err = normalizeEndpoint(raw); err != nil {
			return nil, fmt.Errorf("aws session: %s endpoint: %w", name, err)
		}
	}
	session := &Session{options: options, endpoint: endpoint, serviceEndpoints: serviceEndpoints}
	if _, err := session.newClient("s3"); err != nil {
		return nil, err
	}
	return session, nil
}

// Region is the session's signing region.
func (s *Session) Region() string { return s.options.Region }

// Endpoint is the normalized endpoint override, empty when the AWS endpoints are used.
func (s *Session) Endpoint() string { return s.endpoint }

// Credentials is the provider that signs this session's requests.
func (s *Session) Credentials() awssigv4.CredentialsProvider { return s.options.Credentials }

// normalizeEndpoint validates an override as an http(s) URL with a host and no
// path, query or credentials, and returns it as scheme://host[:port].
func normalizeEndpoint(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid aws endpoint %q: %w", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("invalid aws endpoint %q: want http(s)://host[:port] with no path, query or credentials", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

// dnsSuffix is the partition's DNS suffix for the session region.
func (s *Session) dnsSuffix() string {
	if strings.HasPrefix(s.options.Region, "cn-") {
		return "amazonaws.com.cn"
	}
	return "amazonaws.com"
}

// serviceURL is the base URL for the service whose public hostname starts with host.
func (s *Session) serviceURL(name, host string) string {
	if override := s.serviceEndpoints[name]; override != "" {
		return override
	}
	if s.endpoint != "" && endpointScoped[name] {
		return s.endpoint
	}
	return fmt.Sprintf("https://%s.%s.%s", host, s.options.Region, s.dnsSuffix())
}

// service is one signed HTTP client plus the identity used in errors.
type service struct {
	name    string
	baseURL string
	client  *commonshttp.Client
	// err is a client construction failure, returned by every call.
	err error
}

// newClient builds the signed HTTP client for a service with explicit TLS
// verification settings and the AWS retry strategy.
func (s *Session) newClient(signingName string) (*commonshttp.Client, error) {
	client := commonshttp.NewClient().
		AWSAuthSigV4(awssigv4.Config{Region: s.options.Region, Credentials: s.options.Credentials}).
		AWSService(signingName).
		RetryStrategy(awsRetryStrategy())
	if _, err := client.TLSConfig(commonshttp.TLSConfig{InsecureSkipVerify: s.options.SkipTLSVerify}); err != nil {
		return nil, fmt.Errorf("aws: configuring TLS: %w", err)
	}
	if s.options.Customize != nil {
		client = s.options.Customize(client)
	}
	return client, nil
}

// newService builds the signed client for a service: name labels errors and the
// signing scope, host is the public hostname prefix.
func (s *Session) newService(name, signingName, host string) *service {
	client, err := s.newClient(signingName)
	return &service{name: name, baseURL: s.serviceURL(name, host), client: client, err: err}
}

const (
	retryAttempts = 3
	retryDelay    = 500 * time.Millisecond
)

// awsRetryStrategy retries 429/5xx and transport errors like RetryOnStatus,
// and additionally HTTP 400 responses carrying a throttling error code.
func awsRetryStrategy() commonshttp.RetryStrategy {
	base := commonshttp.RetryOnStatus(retryAttempts, retryDelay, 429, 500, 502, 503, 504)
	return func(resp *commonshttp.Response, err error, attempt int) (bool, time.Duration) {
		if retry, delay := base(resp, err, attempt); retry || err != nil {
			return retry, delay
		}
		if attempt+1 >= retryAttempts || !isThrottled(resp) {
			return false, 0
		}
		return true, retryDelay << attempt
	}
}
