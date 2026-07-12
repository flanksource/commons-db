package context

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/models"
	commons "github.com/flanksource/commons/context"
	"github.com/flanksource/commons/logger"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

func TestEnvCacheIsScopedByKubernetesAuthorization(t *testing.T) {
	envCache.Flush()
	ctxA := cacheTestContext("https://cluster-a", "principal-a", "alpha", "one")
	ctxB := cacheTestContext("https://cluster-b", "principal-b", "bravo", "two")

	secretA, err := GetSecretFromCache(ctxA, "prod", "database", "password")
	if err != nil {
		t.Fatal(err)
	}
	secretB, err := GetSecretFromCache(ctxB, "prod", "database", "password")
	if err != nil {
		t.Fatal(err)
	}
	if secretA != "alpha" || secretB != "bravo" {
		t.Fatalf("secret cache crossed clients: a=%q b=%q", secretA, secretB)
	}

	configA, err := GetConfigMapFromCache(ctxA, "prod", "database", "endpoint")
	if err != nil {
		t.Fatal(err)
	}
	configB, err := GetConfigMapFromCache(ctxB, "prod", "database", "endpoint")
	if err != nil {
		t.Fatal(err)
	}
	if configA != "one" || configB != "two" {
		t.Fatalf("configmap cache crossed clients: a=%q b=%q", configA, configB)
	}
}

func TestWrapPreservesInjectedLocalKubernetesClient(t *testing.T) {
	ctx := cacheTestContext("https://cluster", "principal", "secret", "endpoint")
	want, err := ctx.LocalKubernetes()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctx.Wrap(context.Background()).LocalKubernetes()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatal("Wrap replaced the context-scoped local Kubernetes client")
	}
}

func TestSecretCacheInvalidationObservesRotation(t *testing.T) {
	envCache.Flush()
	ctx := cacheTestContext("https://cluster", "principal", "old", "endpoint")
	client, err := ctx.LocalKubernetes()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := GetSecretFromCache(ctx, "prod", "database", "password"); err != nil || got != "old" {
		t.Fatalf("initial secret = %q, %v", got, err)
	}
	secret, _ := client.CoreV1().Secrets("prod").Get(ctx, "database", metav1.GetOptions{})
	secret.Data["password"] = []byte("new")
	if _, err := client.CoreV1().Secrets("prod").Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := InvalidateSecretCache(ctx, "prod", "database", "password"); err != nil {
		t.Fatal(err)
	}
	if got, err := GetSecretFromCache(ctx, "prod", "database", "password"); err != nil || got != "new" {
		t.Fatalf("rotated secret = %q, %v", got, err)
	}
}

func TestSecretCacheCoalescesConcurrentMisses(t *testing.T) {
	envCache.Flush()
	clientset := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: "prod"},
		Data:       map[string][]byte{"password": []byte("secret")},
	})
	var gets atomic.Int32
	clientset.PrependReactor("get", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		gets.Add(1)
		return false, nil, nil
	})
	client := dutyKubernetes.NewKubeClient(logger.GetLogger("test"), clientset, &rest.Config{Host: "https://cluster", BearerToken: "principal"})
	ctx := Context{Context: commons.NewContext(context.Background())}.WithLocalKubernetes(client)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := GetSecretFromCache(ctx, "prod", "database", "password")
			if err != nil {
				errs <- err
			} else if value != "secret" {
				errs <- fmt.Errorf("unexpected secret %q", value)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if gets.Load() != 1 {
		t.Fatalf("Kubernetes secret GETs = %d, want 1", gets.Load())
	}
}

func TestHydrateConnectionByURLDoesNotCacheResolvedConnection(t *testing.T) {
	db := connectionCacheTestDB(t)
	id := uuid.New()
	connection := models.Connection{ID: id, Name: "database", Namespace: "prod", Type: models.ConnectionTypePostgres, URL: "postgres://first/db"}
	if err := db.Create(&connection).Error; err != nil {
		t.Fatal(err)
	}
	ctx := Context{Context: commons.NewContext(context.Background())}.WithDB(db, nil)
	first, err := HydrateConnectionByURL(ctx, id.String())
	if err != nil {
		t.Fatal(err)
	}
	if first.URL != "postgres://first/db" {
		t.Fatalf("first URL = %q", first.URL)
	}
	if err := db.Model(&models.Connection{}).Where("id = ?", id).Update("url", "postgres://second/db").Error; err != nil {
		t.Fatal(err)
	}
	second, err := HydrateConnectionByURL(ctx, id.String())
	if err != nil {
		t.Fatal(err)
	}
	if second.URL != "postgres://second/db" {
		t.Fatalf("updated URL remained stale: %q", second.URL)
	}
}

func TestKubernetesClientRefreshIsCoalescedAndRaceSafe(t *testing.T) {
	connection := &refreshingKubernetesConnection{}
	ctx := Context{Context: commons.NewContext(context.Background())}
	client, err := NewKubernetesClient(ctx, connection, "test-scope")
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.expiry = time.Now().Add(-time.Hour)
	client.mu.Unlock()

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.Refresh(ctx); err != nil {
				errs <- err
			}
			if client.DutyClient() == nil {
				errs <- fmt.Errorf("refreshed client is nil")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if calls := connection.calls.Load(); calls != 2 {
		t.Fatalf("populate calls = %d, want initial + one refresh", calls)
	}
}

type refreshingKubernetesConnection struct{ calls atomic.Int32 }

func (c *refreshingKubernetesConnection) Populate(Context, bool) (kubernetes.Interface, *rest.Config, error) {
	c.calls.Add(1)
	return fake.NewSimpleClientset(), &rest.Config{Host: "https://cluster", BearerToken: "token"}, nil
}

func (*refreshingKubernetesConnection) Hash() string    { return "safe-hash" }
func (*refreshingKubernetesConnection) CanExpire() bool { return true }
func (*refreshingKubernetesConnection) String() string  { return "test" }

func cacheTestContext(host, token, secretValue, configValue string) Context {
	clientset := fake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: "prod"}, Data: map[string][]byte{"password": []byte(secretValue)}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: "prod"}, Data: map[string]string{"endpoint": configValue}},
	)
	client := dutyKubernetes.NewKubeClient(logger.GetLogger("test"), clientset, &rest.Config{Host: host, BearerToken: token})
	return Context{Context: commons.NewContext(context.Background())}.WithLocalKubernetes(client)
}

func connectionCacheTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE connections (
        id TEXT PRIMARY KEY, name TEXT, namespace TEXT, source TEXT, type TEXT,
        url TEXT, username TEXT, password TEXT, properties TEXT, certificate TEXT,
        insecure_tls NUMERIC, created_at DATETIME, updated_at DATETIME, created_by TEXT
    )`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}
