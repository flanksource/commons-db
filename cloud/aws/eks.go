package aws

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/flanksource/commons/http/middlewares/awssigv4"
)

const (
	// EKSClusterIDHeader names the cluster a token is minted for.
	EKSClusterIDHeader = "x-k8s-aws-id"
	eksTokenPrefix     = "k8s-aws-v1."
	emptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	// eksPresignRegion and eksPresignHost are fixed: the global STS endpoint
	// validates tokens signed for us-east-1.
	eksPresignRegion  = "us-east-1"
	eksPresignHost    = "sts.amazonaws.com"
	eksPresignExpires = 24 * time.Hour
)

// EKS is the Elastic Kubernetes Service client (REST-JSON).
type EKS struct{ svc *service }

// EKS returns the EKS client.
func (s *Session) EKS() *EKS { return &EKS{svc: s.newService("eks", "eks", "eks")} }

// EKSCluster is the part of DescribeCluster the kube client needs.
type EKSCluster struct {
	Endpoint string
	// CertificateAuthorityData is the base64-encoded PEM cluster CA, as EKS returns it.
	CertificateAuthorityData string
}

// DescribeCluster returns the cluster endpoint and CA.
func (c *EKS) DescribeCluster(ctx context.Context, name string) (*EKSCluster, error) {
	var out struct {
		Cluster struct {
			Endpoint             string `json:"endpoint"`
			CertificateAuthority struct {
				Data string `json:"data"`
			} `json:"certificateAuthority"`
		} `json:"cluster"`
	}
	err := c.svc.do(ctx, call{Operation: "DescribeCluster", Method: http.MethodGet, Path: escapePath("clusters", name)}, &out, decodeJSON)
	if err != nil {
		return nil, err
	}
	return &EKSCluster{Endpoint: out.Cluster.Endpoint, CertificateAuthorityData: out.Cluster.CertificateAuthority.Data}, nil
}

// EKSToken is the bearer token kubectl-style auth sends: "k8s-aws-v1." followed
// by the base64url of a presigned STS GetCallerIdentity URL that is bound to the
// cluster through the x-k8s-aws-id header.
func EKSToken(credentials awssigv4.Credentials, cluster string, now time.Time) (string, error) {
	signed, err := PresignGetCallerIdentity(credentials, cluster, now)
	if err != nil {
		return "", err
	}
	return eksTokenPrefix + base64.URLEncoding.EncodeToString([]byte(signed)), nil
}

// PresignGetCallerIdentity presigns STS GetCallerIdentity with query-string
// SigV4. The signed headers are host and x-k8s-aws-id; the payload is empty.
func PresignGetCallerIdentity(credentials awssigv4.Credentials, cluster string, now time.Time) (string, error) {
	if credentials.AccessKeyID == "" || credentials.SecretAccessKey == "" {
		return "", fmt.Errorf("presigning requires an access key and secret key")
	}
	if cluster == "" {
		return "", fmt.Errorf("presigning an EKS token requires a cluster name")
	}
	stamp := now.UTC().Format("20060102T150405Z")
	scope := stamp[:8] + "/" + eksPresignRegion + "/sts/aws4_request"
	query := url.Values{
		"Action":              {"GetCallerIdentity"},
		"Version":             {"2011-06-15"},
		"X-Amz-Algorithm":     {"AWS4-HMAC-SHA256"},
		"X-Amz-Credential":    {credentials.AccessKeyID + "/" + scope},
		"X-Amz-Date":          {stamp},
		"X-Amz-Expires":       {fmt.Sprint(int(eksPresignExpires.Seconds()))},
		"X-Amz-SignedHeaders": {"host;" + EKSClusterIDHeader},
	}
	if credentials.SessionToken != "" {
		query.Set("X-Amz-Security-Token", credentials.SessionToken)
	}
	canonicalQuery := query.Encode() // url.Values.Encode sorts by key
	canonical := "GET\n/\n" + canonicalQuery + "\nhost:" + eksPresignHost + "\n" + EKSClusterIDHeader + ":" + cluster + "\n\nhost;" + EKSClusterIDHeader + "\n" + emptyPayloadSHA256
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + sha256Hex(canonical)
	signature := hex.EncodeToString(hmacSHA256(signingKey(credentials.SecretAccessKey, stamp[:8]), toSign))
	return "https://" + eksPresignHost + "/?" + canonicalQuery + "&X-Amz-Signature=" + signature, nil
}

func signingKey(secret, date string) []byte {
	key := hmacSHA256([]byte("AWS4"+secret), date)
	key = hmacSHA256(key, eksPresignRegion)
	key = hmacSHA256(key, "sts")
	return hmacSHA256(key, "aws4_request")
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
