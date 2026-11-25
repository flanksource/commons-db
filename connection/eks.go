package connection

import (
	gocontext "context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
	"github.com/flanksource/commons-db/context"
	"github.com/patrickmn/go-cache"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// tokenSafetyMargin is the duration to deduct when caching tokens.
// Example: if a token expires in 15 minutes then we cache it with a
// TTL of 15 minutes - tokenSafetyMargin = 10 minutes.
//
// This is to prevent using a token that immediately expires.
const tokenSafetyMargin = time.Minute * 5

// caches cloud tokens. eg: EKS token, GKE token, ...
var tokenCache = cache.New(time.Hour, time.Hour)

func tokenCacheKey(cloud string, cred any, identifiers string) string {
	switch v := cred.(type) {
	case string:
		return fmt.Sprintf("%s-%s-%s", cloud, v, identifiers)
	default:
		m, _ := json.Marshal(v)
		return fmt.Sprintf("%s-%s-%s", cloud, m, identifiers)
	}
}

// +kubebuilder:object:generate=true
type EKSConnection struct {
	AWSConnection `json:",inline" yaml:",inline"`

	Cluster string `json:"cluster"`
}

func (t *EKSConnection) Populate(ctx ConnectionContext) error {
	return t.AWSConnection.Populate(ctx)
}

func (t *EKSConnection) KubernetesClient(ctx context.Context, freshToken bool) (kubernetes.Interface, *rest.Config, error) {
	session, err := t.AWSConnection.Client(ctx)
	if err != nil {
		return nil, nil, err
	}

	eksEndpoint, ca, err := eksClusterDetails(ctx, t.Cluster, session)
	if err != nil {
		return nil, nil, err
	}

	token, err := getEKSToken(ctx, t.Cluster, session, freshToken)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get token for EKS: %w", err)
	}

	restConfig := &rest.Config{
		Host:        eksEndpoint,
		BearerToken: token,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: ca,
		},
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, err
	}

	return clientset, restConfig, nil
}

func eksClusterDetails(ctx gocontext.Context, clusterName string, session *cloudaws.Session) (string, []byte, error) {
	cluster, err := session.EKS().DescribeCluster(ctx, clusterName)
	if err != nil {
		return "", nil, fmt.Errorf("unable to get cluster info: %w", err)
	}

	ca, err := base64.StdEncoding.DecodeString(cluster.CertificateAuthorityData)
	if err != nil {
		return "", nil, fmt.Errorf("unable to base64 decode ca: %w", err)
	}

	return cluster.Endpoint, ca, nil
}

func getEKSToken(ctx gocontext.Context, cluster string, session *cloudaws.Session, freshToken bool) (string, error) {
	cred, err := session.Credentials().Retrieve(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to retrive credentials from aws session: %w", err)
	}

	cacheKey := tokenCacheKey("eks", cred, cluster)
	if !freshToken {
		if found, ok := tokenCache.Get(cacheKey); ok {
			return found.(string), nil
		}
	}

	token, err := cloudaws.EKSToken(cred, cluster, time.Now())
	if err != nil {
		return "", fmt.Errorf("failed to get signed URI: %w", err)
	}

	tokenTTL := time.Minute * 15
	tokenCache.Set(cacheKey, token, tokenTTL-tokenSafetyMargin)
	return token, nil
}
