package context

import (
	"fmt"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/pkg/kube/auth"
	"github.com/flanksource/commons/logger"
	"github.com/golang-jwt/jwt/v5"
	"github.com/samber/lo"
)

type KubernetesClient struct {
	*dutyKubernetes.Client
	Connection KubernetesConnection
	expiry     time.Time
	logger     logger.Logger
	mu         sync.Mutex
	cacheKey   string
}

var defaultExpiry = 15 * time.Minute

func authProvider(clusterAddress string, config map[string]string, persister rest.AuthProviderConfigPersister) (rest.AuthProvider, error) {
	connHash := config["conn"]
	if connHash == "" {
		return nil, fmt.Errorf("key[conn] with connection hash not set")
	}
	ap, err := auth.GetAuthenticator(connHash)
	return ap, err
}

func NewKubernetesClient(ctx Context, conn KubernetesConnection, cacheKeys ...string) (*KubernetesClient, error) {
	cacheKey := conn.Hash()
	if len(cacheKeys) > 0 && cacheKeys[0] != "" {
		cacheKey = cacheKeys[0]
	}
	c, rc, err := conn.Populate(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("error refreshing kubernetes client: %w", err)
	}
	log := logger.GetLogger("k8s." + conn.String())
	client := &KubernetesClient{
		Client:     dutyKubernetes.NewKubeClient(log, c, rc),
		Connection: conn,
		logger:     logger.GetLogger("k8s"),
		cacheKey:   cacheKey,
	}

	if client.logger.IsLevelEnabled(logger.Trace4) {
		client.logger.V(logger.Trace1).Infof(logger.Stacktrace())
	}

	client.setExpiryLocked(defaultExpiry)

	connHash := cacheKey
	if rc.ExecProvider == nil && rc.BearerToken != "" {
		refreshCallback := func() (*rest.Config, error) {
			ctx.Counter("kubernetes_auth_plugin_refreshed", "connection", connHash).Add(1)
			rc, err := client.Refresh(ctx)
			return rc, err
		}
		rc.BearerToken = ""
		if err := auth.AuthKubernetesCallbackCache.Set(ctx, connHash, refreshCallback); err != nil {
			return nil, err
		}
		rc.AuthProvider = &clientcmdapi.AuthProviderConfig{
			Name:   "duty",
			Config: map[string]string{"conn": conn.Hash()},
		}
	}

	client.SetLogger(logger.GetLogger("k8s." + dutyKubernetes.GetClusterName(rc)))

	client.logger.V(3).Infof("created new client with expiry: %s", client.expiry.Format(time.RFC3339))
	return client, nil
}

func (c *KubernetesClient) SetLogger(log logger.Logger) {
	c.logger = log
	c.Client.SetLogger(log)
}

func (c *KubernetesClient) setExpiryLocked(def time.Duration) {
	// Try parsing BearerToken as JWT and extract expiry
	if expiry := extractExpiryFromJWT(lo.FromPtr(c.Config).BearerToken); !expiry.IsZero() {
		c.expiry = expiry
	} else {
		c.expiry = time.Now().Add(def)
	}
}

func (c *KubernetesClient) Refresh(ctx Context) (*rest.Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hasExpiredLocked() && (c.Config.AuthProvider == nil || c.Config.BearerToken != "") {
		return c.RestConfig(), nil
	}
	clientset, rc, err := c.Connection.Populate(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("error refreshing kubernetes client: %w", err)
	}

	// Swap the client atomically instead of mutating a rest.Config or clientset
	// already in use by concurrent callers.
	c.Client = dutyKubernetes.NewKubeClient(c.logger, clientset, rc)
	c.setExpiryLocked(defaultExpiry)
	c.logger.V(5).Infof("token refreshed, expires at %s", c.expiry.Format(time.RFC3339))
	return rc, nil
}

func (c *KubernetesClient) HasExpired() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hasExpiredLocked()
}

func (c *KubernetesClient) hasExpiredLocked() bool {
	if c.Connection.CanExpire() && !c.expiry.IsZero() {
		// We give a 1 minute window as a buffer
		return time.Until(c.expiry) <= time.Minute
	}
	return false
}

func (c *KubernetesClient) DutyClient() *dutyKubernetes.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Client
}

func extractExpiryFromJWT(token string) time.Time {
	claims := jwt.MapClaims{}
	// Ignore errors since it can be an invalid token as well
	_, _, _ = jwt.NewParser().ParseUnverified(token, claims)
	if t, _ := claims.GetExpirationTime(); t != nil {
		return t.Time
	}
	return time.Time{}
}

func init() {
	_ = rest.RegisterAuthProviderPlugin("duty", authProvider)
}
