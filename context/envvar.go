package context

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"dario.cat/mergo"
	"github.com/ohler55/ojg/jp"
	"github.com/samber/lo"

	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/types"
	"github.com/flanksource/commons/logger"
	"github.com/patrickmn/go-cache"
	"golang.org/x/sync/singleflight"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const defaultEnvCacheTTL = time.Minute

// envCache is scoped in every key by the Kubernetes API and authorization
// fingerprint. This prevents a value fetched by one cluster or principal from
// satisfying a lookup made by another.
var (
	envCache       = cache.New(defaultEnvCacheTTL, 2*defaultEnvCacheTTL)
	envLookupGroup singleflight.Group
)

const helmSecretType = "helm.sh/release.v1"

func GetEnvValueFromCache(ctx Context, input types.EnvVar, namespace string) (value string, err error) {
	if input.IsEmpty() {
		return "", nil
	}
	ctx, cancel := ctx.WithTimeout(ctx.Properties().Duration("envvar.lookup.timeout", 5*time.Second))
	defer cancel()
	if namespace == "" {
		namespace = ctx.GetNamespace()
	}
	var source = ""

	if input.ValueFrom == nil {
		source = "static"
		value = input.ValueStatic
	} else if input.ValueFrom.SecretKeyRef != nil && !input.ValueFrom.SecretKeyRef.IsEmpty() {
		source = fmt.Sprintf("secret(%s/%s).%s", namespace, input.ValueFrom.SecretKeyRef.Name, input.ValueFrom.SecretKeyRef.Key)
		value, err = GetSecretFromCache(ctx, namespace, input.ValueFrom.SecretKeyRef.Name, input.ValueFrom.SecretKeyRef.Key)
	} else if input.ValueFrom.ConfigMapKeyRef != nil && !input.ValueFrom.ConfigMapKeyRef.IsEmpty() {
		source = fmt.Sprintf("configmap(%s/%s).%s", namespace, input.ValueFrom.ConfigMapKeyRef.Name, input.ValueFrom.ConfigMapKeyRef.Key)
		value, err = GetConfigMapFromCache(ctx, namespace, input.ValueFrom.ConfigMapKeyRef.Name, input.ValueFrom.ConfigMapKeyRef.Key)
	} else if input.ValueFrom.HelmRef != nil && !input.ValueFrom.HelmRef.IsEmpty() {
		source = fmt.Sprintf("helm(%s/%s).%s", namespace, input.ValueFrom.HelmRef.Name, input.ValueFrom.HelmRef.Key)
		value, err = GetHelmValueFromCache(ctx, namespace, input.ValueFrom.HelmRef.Name, input.ValueFrom.HelmRef.Key)
	} else if !lo.IsEmpty(input.ValueFrom.ServiceAccount) {
		source = fmt.Sprintf("service-account(%s/%s)", namespace, *input.ValueFrom.ServiceAccount)
		value, err = GetServiceAccountTokenFromCache(ctx, namespace, *input.ValueFrom.ServiceAccount)
	} else if !lo.IsEmpty(input.ValueFrom.OnePassword) {
		source = fmt.Sprintf("1password(%s)", *input.ValueFrom.OnePassword)
		value, err = GetOnePasswordValueFromCache(ctx, *input.ValueFrom.OnePassword)
	}

	if err != nil {
		ctx.Logger.V(3).Infof("lookup[%s] failed %s => %s", input.Name, source, err.Error())
	} else if ctx.Logger.IsLevelEnabled(5) {
		ctx.Logger.V(5).Infof("lookup[%s] %s => %s", input.Name, source, logger.PrintableSecret(value))
	}

	return value, err
}

func GetEnvStringFromCache(ctx Context, env string, namespace string) (string, error) {
	var envvar types.EnvVar
	if err := envvar.Scan(env); err != nil {
		return "", err
	}
	return GetEnvValueFromCache(ctx, envvar, namespace)
}

// debugJsonPath is a helper function to visualize the result of a jsonpath expression on some data
// it splits the jsonpath into parts, and then applies each part sequentially, printing the intermediate success
// and highlighting which part failed, and what the available keys where at the failure point
//
//	jsonPath: "spec.template.spec.containers[0].image"
//
// spec: ✓
// template: ✓
// spec: ✓
// containers: ✓
// [0]: ✓
// image: ✖ available keys: [name image ports]
func debugJsonPath(key string, data any) string {
	parts := strings.Split(key, ".")
	current := data
	var result strings.Builder
	for _, part := range parts {
		jpExpr, err := jp.ParseString(part)
		if err != nil {
			result.WriteString(fmt.Sprintf("%s: ✖ could not parse jsonpath expression: %s\n", part, err))
			break
		}
		values := jpExpr.Get(current)
		if len(values) == 0 {
			switch v := current.(type) {
			case map[string]any:
				result.WriteString(fmt.Sprintf("%s: ✖ available keys: [%s]\n", part, strings.Join(lo.Keys(v), ", ")))
			default:
				result.WriteString(fmt.Sprintf("%s: ✖ could not find key in current data\n", part))
			}
			break
		} else {
			result.WriteString(fmt.Sprintf("%s: ✓\n", part))
			current = values[0]
		}
	}
	return result.String()

}

func GetHelmValuesFromCache(ctx Context, namespace, releaseName string) (map[string]any, error) {

	client, err := ctx.LocalKubernetes()
	if err != nil {
		return nil, fmt.Errorf("error creating kubernetes client: %w", err)
	}

	secretList, err := client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("type=%s", helmSecretType),
		LabelSelector: fmt.Sprintf("status=deployed,name=%s", releaseName),
		Limit:         1,
	})
	if err != nil {
		return nil, fmt.Errorf("could not get secrets in namespace: %s: %w", namespace, err)
	}

	if len(secretList.Items) == 0 {
		return nil, fmt.Errorf("a deployed helm secret was not found %s/%s", namespace, releaseName)
	}
	secret := secretList.Items[0]

	if secret.Name == "" {
		return nil, fmt.Errorf("could not find helm secret %s/%s", namespace, releaseName)
	}

	release, err := base64.StdEncoding.DecodeString(string(secret.Data["release"]))
	if err != nil {
		return nil, fmt.Errorf("could not base64 decode helm secret %s/%s: %w", namespace, secret.Name, err)
	}

	gzipReader, err := gzip.NewReader(bytes.NewReader(release))
	if err != nil {
		return nil, fmt.Errorf("could not unzip helm secret %s/%s: %w", namespace, secret.Name, err)
	}

	var rawJson map[string]any
	if err := json.NewDecoder(gzipReader).Decode(&rawJson); err != nil {
		return nil, fmt.Errorf("could not decode unzipped helm secret %s/%s: %w", namespace, secret.Name, err)
	}

	var chartValues any = map[string]any{}
	if chart, ok := rawJson["chart"].(map[string]any); ok {
		chartValues = chart["values"]
	}
	merged := rawJson["config"].(map[string]any)

	if err := mergo.Merge(&merged, chartValues); err != nil {
		return nil, fmt.Errorf("could not merge helm config and values of helm secret %s/%s: %w", namespace, secret.Name, err)
	}
	return merged, nil
}
func GetHelmValueFromCache(ctx Context, namespace, releaseName, key string) (string, error) {
	_, scope, err := localKubernetesCacheScope(ctx)
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("%s/helm/%s/%s/%s", scope, namespace, releaseName, key)
	if value, found := envCache.Get(id); found {
		return value.(string), nil
	}
	value, err, _ := envLookupGroup.Do(id, func() (any, error) {
		merged, err := GetHelmValuesFromCache(ctx, namespace, releaseName)
		if err != nil {
			return "", err
		}
		keyJPExpr, err := jp.ParseString(key)
		if err != nil {
			return "", fmt.Errorf("could not parse key:%s. must be a valid jsonpath expression. %w", key, err)
		}
		results := keyJPExpr.Get(merged)
		if len(results) == 0 {
			return "", fmt.Errorf("could not find key %s in merged helm secret %s/%s", key, namespace, lo.Keys(merged))
		}
		val := ""
		if len(results) == 1 {
			switch v := results[0].(type) {
			case string:
				val = v
			case []byte:
				val = string(v)
			case int, int32, int64:
				val = fmt.Sprintf("%d", v)
			case float32, float64:
				val = fmt.Sprintf("%0f", v)
			default:
				b, err := json.Marshal(v)
				if err != nil {
					return "", fmt.Errorf("could not marshal merged helm secret %s/%s: %w", namespace, releaseName, err)
				}
				val = string(b)
			}
		}
		envCache.Set(id, val, envCacheTTL(ctx, "envvar.helm.cache.timeout"))
		return val, nil
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func GetSecretFromCache(ctx Context, namespace, name, key string) (string, error) {
	client, scope, err := localKubernetesCacheScope(ctx)
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("%s/secret/%s/%s/%s", scope, namespace, name, key)
	if value, found := envCache.Get(id); found {
		return value.(string), nil
	}
	value, err, _ := envLookupGroup.Do(id, func() (any, error) {
		secret, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("could not find secret %s/%s: %s", namespace, name, err)
		}
		if secret == nil {
			return "", fmt.Errorf("could not get contents of secret %s/%s", namespace, name)
		}
		resolved, ok := secret.Data[key]
		if !ok {
			return "", fmt.Errorf("could not find key %v in secret %s/%s (%s)", key, namespace, name, strings.Join(lo.Keys(secret.Data), ", "))
		}
		envCache.Set(id, string(resolved), envCacheTTL(ctx, "envvar.cache.timeout"))
		return string(resolved), nil
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func GetConfigMapFromCache(ctx Context, namespace, name, key string) (string, error) {
	client, scope, err := localKubernetesCacheScope(ctx)
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("%s/cm/%s/%s/%s", scope, namespace, name, key)
	if value, found := envCache.Get(id); found {
		return value.(string), nil
	}
	value, err, _ := envLookupGroup.Do(id, func() (any, error) {
		configMap, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("could not get configmap %s/%s: %s", namespace, name, err)
		}
		if configMap == nil {
			return "", fmt.Errorf("could not get contents of configmap %s/%s", namespace, name)
		}
		resolved, ok := configMap.Data[key]
		if !ok {
			return "", fmt.Errorf("could not find key %v in configmap %s/%s (%s)", key, namespace, name,
				strings.Join(lo.Keys(configMap.Data), ", "))
		}
		envCache.Set(id, resolved, envCacheTTL(ctx, "envvar.cache.timeout"))
		return resolved, nil
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func GetServiceAccountTokenFromCache(ctx Context, namespace, serviceAccount string) (string, error) {
	client, scope, err := localKubernetesCacheScope(ctx)
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("%s/sa-token/%s/%s", scope, namespace, serviceAccount)
	if value, found := envCache.Get(id); found {
		return value.(string), nil
	}
	value, err, _ := envLookupGroup.Do(id, func() (any, error) {
		tokenRequest, err := client.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, serviceAccount, &authenticationv1.TokenRequest{}, metav1.CreateOptions{})
		if err != nil {
			return "", fmt.Errorf("could not get token for service account %s/%s: %w", namespace, serviceAccount, err)
		}
		ttl := time.Until(tokenRequest.Status.ExpirationTimestamp.Time) - time.Minute
		if ttl <= 0 {
			return "", fmt.Errorf("service account token for %s/%s expires too soon", namespace, serviceAccount)
		}
		envCache.Set(id, tokenRequest.Status.Token, ttl)
		return tokenRequest.Status.Token, nil
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func localKubernetesCacheScope(ctx Context) (*dutyKubernetes.Client, string, error) {
	client, err := ctx.LocalKubernetes()
	if err != nil {
		return nil, "", fmt.Errorf("error creating kubernetes client: %w", err)
	}
	scope := dutyKubernetes.RestConfigFingerprint(client.RestConfig())
	if scope == "" {
		scope = fmt.Sprintf("client-%p", client)
	}
	return client, scope, nil
}

func envCacheTTL(ctx Context, override string) time.Duration {
	base := ctx.Properties().Duration("envvar.cache.timeout", defaultEnvCacheTTL)
	if override != "" {
		return ctx.Properties().Duration(override, base)
	}
	return base
}

// InvalidateSecretCache and InvalidateConfigMapCache provide an explicit hook
// for controllers that observe Kubernetes resource-version changes.
func InvalidateSecretCache(ctx Context, namespace, name, key string) error {
	_, scope, err := localKubernetesCacheScope(ctx)
	if err != nil {
		return err
	}
	envCache.Delete(fmt.Sprintf("%s/secret/%s/%s/%s", scope, namespace, name, key))
	return nil
}

func InvalidateConfigMapCache(ctx Context, namespace, name, key string) error {
	_, scope, err := localKubernetesCacheScope(ctx)
	if err != nil {
		return err
	}
	envCache.Delete(fmt.Sprintf("%s/cm/%s/%s/%s", scope, namespace, name, key))
	return nil
}

func (ctx Context) Lookup(namespace string) *EnvVarSourceBuilder {
	return &EnvVarSourceBuilder{
		envVarSource: &types.EnvVarSource{},
		context:      ctx,
		namespace:    namespace,
	}
}

type EnvVarSourceBuilder struct {
	envVarSource *types.EnvVarSource
	context      Context
	namespace    string
}

func (b *EnvVarSourceBuilder) MustGetString() string {
	value, err := b.GetString()
	if err != nil {
		panic(err)
	}
	return value
}

func (b *EnvVarSourceBuilder) GetString() (string, error) {

	return GetEnvValueFromCache(b.context, types.EnvVar{ValueFrom: b.envVarSource}, b.namespace)
}

func (b *EnvVarSourceBuilder) WithServiceAccount(name string) *EnvVarSourceBuilder {
	b.envVarSource.ServiceAccount = &name
	return b
}

func (b *EnvVarSourceBuilder) WithHelmRef(name, key string) *EnvVarSourceBuilder {
	b.envVarSource.HelmRef = &types.HelmRefKeySelector{
		LocalObjectReference: types.LocalObjectReference{Name: name},
		Key:                  key,
	}
	return b
}

func (b *EnvVarSourceBuilder) WithConfigMapKeyRef(name, key string) *EnvVarSourceBuilder {
	b.envVarSource.ConfigMapKeyRef = &types.ConfigMapKeySelector{
		LocalObjectReference: types.LocalObjectReference{Name: name},
		Key:                  key,
	}
	return b
}

func (b *EnvVarSourceBuilder) WithSecretKeyRef(name, key string) *EnvVarSourceBuilder {
	b.envVarSource.SecretKeyRef = &types.SecretKeySelector{
		LocalObjectReference: types.LocalObjectReference{Name: name},
		Key:                  key,
	}
	return b
}
