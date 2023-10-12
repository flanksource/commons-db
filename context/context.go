package context

import (
	commons "github.com/flanksource/commons/context"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/flanksource/duty"
	"github.com/flanksource/duty/models"
	"github.com/flanksource/duty/types"

	"k8s.io/client-go/kubernetes"
)

type Context struct {
	*commons.Context
}

func NewContext() *Context {
	return &Context{
		Context: commons.NewContext(
			commons.WithDebugFn(func(ctx *commons.Context) bool {
				annotations := getObjectMeta(ctx).Annotations
				return annotations != nil && (annotations["debug"] == "true" || annotations["trace"] == "true")

			}),
			commons.WithTraceFn(func(ctx *commons.Context) bool {
				annotations := getObjectMeta(ctx).Annotations
				return annotations != nil && annotations["trace"] == "true"
			}),
		),
	}
}

func (k *Context) WithObject(object metav1.ObjectMeta) *Context {
	return &Context{
		Context: k.WithValue("object", object),
	}
}

func (k *Context) WithUser(user *models.Person) *Context {
	return &Context{
		Context: k.WithValue("user", user),
	}
}

func (k *Context) User() *models.Person {
	return k.Value("user").(*models.Person)
}

func (k *Context) WithKubernetes(client kubernetes.Interface) *Context {
	return &Context{
		Context: k.WithValue("kubernetes", client),
	}
}

func (k Context) WithDB(db *gorm.DB, pool *pgxpool.Pool) *Context {
	return &Context{
		Context: k.WithValue("db", db).WithValue("pgxpool", pool),
	}
}

func (k Context) DB() *gorm.DB {
	return k.Value("db").(*gorm.DB)
}

func (k Context) PgxPool() *pgxpool.Pool {
	return k.Value("pgxpool").(*pgxpool.Pool)
}

func (k *Context) Kubernetes() kubernetes.Interface {
	return k.Value("kubernetes").(kubernetes.Interface)
}

func (k Context) StartSpan(name string) (Context, trace.Span) {
	ctx, span := k.Context.StartSpan(name)
	span.SetAttributes(
		attribute.String("name", k.GetName()),
		attribute.String("namespace", k.GetNamespace()),
	)

	return Context{
		Context: ctx,
	}, span
}

func getObjectMeta(ctx *commons.Context) metav1.ObjectMeta {
	o := ctx.Value("object")
	if o == nil {
		return metav1.ObjectMeta{Annotations: map[string]string{}, Labels: map[string]string{}}
	}
	return o.(metav1.ObjectMeta)
}

func (k Context) GetObjectMeta() metav1.ObjectMeta {
	return getObjectMeta(k.Context)
}

func (k Context) GetNamespace() string {
	return k.GetObjectMeta().Namespace
}
func (k Context) GetName() string {
	return k.GetObjectMeta().Name
}

func (k Context) GetLabels() map[string]string {
	return k.GetObjectMeta().Labels
}

func (k Context) GetAnnotations() map[string]string {
	return k.GetObjectMeta().Annotations
}

func (k *Context) GetEnvValueFromCache(c kubernetes.Interface, input types.EnvVar, namespace string) (string, error) {
	return duty.GetEnvValueFromCache(k.Kubernetes(), input, namespace)
}

func (k *Context) GetEnvStringFromCache(env string, namespace string) (string, error) {
	return duty.GetEnvStringFromCache(k.Kubernetes(), env, namespace)
}

func (k *Context) GetSecretFromCache(namespace, name, key string) (string, error) {
	return duty.GetSecretFromCache(k.Kubernetes(), namespace, name, key)
}

func (k *Context) GetConfigMapFromCache(namespace, name, key string) (string, error) {
	return duty.GetConfigMapFromCache(k.Kubernetes(), namespace, name, key)
}

func (k *Context) HydratedConnectionByURL(namespace, connectionString string) (*models.Connection, error) {
	return duty.HydratedConnectionByURL(k, k.DB(), k.Kubernetes(), namespace, connectionString)
}

func (k *Context) HydrateConnection(connection *models.Connection, namespace string) (*models.Connection, error) {
	return duty.HydrateConnection(k, k.Kubernetes(), k.DB(), connection, namespace)
}
