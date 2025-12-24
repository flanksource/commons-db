package kubernetes

import (
	"bufio"
	"bytes"
	"container/list"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
	"github.com/flanksource/commons-db/cache"
	"github.com/flanksource/commons-db/types"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons/properties"
	"github.com/flanksource/commons/timer"
	"github.com/flanksource/is-healthy/pkg/health"
	"github.com/samber/lo"
	"golang.org/x/sync/errgroup"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	cachev4 "github.com/eko/gocache/lib/v4/cache"
	"k8s.io/client-go/discovery/cached/disk"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/remotecommand"
)

type gvkClientResourceCacheValue struct {
	gvr     schema.GroupVersionResource
	mapping *meta.RESTMapping
}

type Client struct {
	kubernetes.Interface
	defaultNamespace       string
	restMapper             *restmapper.DeferredDiscoveryRESTMapper
	dynamicClient          *dynamic.DynamicClient
	Config                 *rest.Config // Prefer updaating token in place
	gvkClientResourceCache cachev4.CacheInterface[gvkClientResourceCacheValue]
	logger                 logger.Logger
}

func (c *Client) SetLogger(logger logger.Logger) {
	c.logger = logger
}

func NewKubeClient(logger logger.Logger, client kubernetes.Interface, config *rest.Config) *Client {
	return &Client{
		Interface:              client,
		defaultNamespace:       "default",
		Config:                 config,
		gvkClientResourceCache: cache.NewCache[gvkClientResourceCacheValue]("gvk-cache", 24*time.Hour),
		logger:                 logger,
	}
}

func (c *Client) Reset() {
	c.dynamicClient = nil
	c.defaultNamespace = "default"
}

func (c *Client) WithNamespace(namespace string) *Client {
	newClient := *c
	newClient.defaultNamespace = namespace
	return &newClient
}

func (c *Client) ResetRestMapper() {
	c.restMapper.Reset()
}

func (c *Client) FetchResources(
	ctx context.Context,
	resources ...unstructured.Unstructured,
) ([]unstructured.Unstructured, error) {
	if len(resources) == 0 {
		return nil, nil
	}

	eg, ctx := errgroup.WithContext(ctx)
	items := make(chan unstructured.Unstructured, len(resources))
	for i := range resources {
		resource := resources[i]
		client, err := c.GetClientByGroupVersionKind(
			ctx,
			resource.GroupVersionKind().Group,
			resource.GroupVersionKind().Version,
			resource.GetKind(),
		)
		if err != nil {
			return nil, err
		}

		eg.Go(func() error {
			item, err := client.Namespace(resource.GetNamespace()).Get(ctx, resource.GetName(), metav1.GetOptions{})
			if err != nil {
				return err
			}

			items <- *item
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	output, _, _, _ := lo.Buffer(items, len(items)) //nolint:dogsled
	return output, nil
}

func (c *Client) GetClientByGroupVersionKind(
	ctx context.Context, group, version, kind string,
) (dynamic.NamespaceableResourceInterface, error) {
	dynamicClient, err := c.GetDynamicClient()
	if err != nil {
		return nil, err
	}

	cacheKey := group + version + kind
	if res, err := c.gvkClientResourceCache.Get(ctx, cacheKey); err == nil {
		return dynamicClient.Resource(res.gvr), nil
	}

	rm, _ := c.GetRestMapper()
	gvk, err := rm.KindFor(schema.GroupVersionResource{
		Resource: kind,
		Group:    group,
		Version:  version,
	})
	if err != nil {
		return nil, err
	}

	gk := schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}
	mapping, err := rm.RESTMapping(gk, gvk.Version)
	if err != nil {
		return nil, err
	}

	_ = c.gvkClientResourceCache.Set(ctx, cacheKey, gvkClientResourceCacheValue{gvr: mapping.Resource, mapping: mapping})
	return dynamicClient.Resource(mapping.Resource), nil
}

func (c *Client) RestConfig() *rest.Config {
	return c.Config
}

func (c *Client) Get(ctx context.Context, kind, namespace, name string) (*unstructured.Unstructured, error) {
	client, _, err := c.GetClientByKind(kind)
	if err != nil {
		return nil, err
	}

	resource, err := client.Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return resource, nil
}

func (c *Client) List(ctx context.Context, kind, namespace, selector string) ([]unstructured.Unstructured, error) {
	client, _, err := c.GetClientByKind(kind)
	if err != nil {
		return nil, err
	}

	resource, err := client.Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}

	return resource.Items, nil
}

// WARN: "Kind" is not specific enough.
// A cluster can have various resources with the same Kind.
// example: helmchrats.helm.cattle.io & helmcharts.source.toolkit.fluxcd.io both have HelmChart as the kind.
//
// Use GetClientByGroupVersionKind instead.
func (c *Client) GetClientByKind(kind string) (dynamic.NamespaceableResourceInterface, *meta.RESTMapping, error) {
	dynamicClient, err := c.GetDynamicClient()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get dynamic client: %w", err)
	}

	if res, err := c.gvkClientResourceCache.Get(context.Background(), kind); err == nil {
		return dynamicClient.Resource(res.gvr), res.mapping, nil
	}

	rm, _ := c.GetRestMapper()
	gvk, err := rm.KindFor(schema.GroupVersionResource{
		Resource: kind,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get kind for %s: %w", kind, err)
	}

	gk := schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}
	mapping, err := rm.RESTMapping(gk, gvk.Version)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get rest mapping for %s: %w", kind, err)
	}

	if err := c.gvkClientResourceCache.Set(context.Background(), kind, gvkClientResourceCacheValue{gvr: mapping.Resource, mapping: mapping}); err != nil {
		c.logger.Errorf("failed to set gvk cache for %s: %s", kind, err)
	}

	return dynamicClient.Resource(mapping.Resource), mapping, nil
}

func (c *Client) DeleteByGVK(ctx context.Context, namespace, name string, gvk schema.GroupVersionKind) (bool, error) {
	client, err := c.GetClientByGroupVersionKind(ctx, gvk.Group, gvk.Version, gvk.Kind)
	if err != nil {
		return false, err
	}

	if err := client.Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		if apiErrors.IsNotFound(err) {
			return false, nil
		}
	}

	return true, nil
}

// GetDynamicClient creates a new k8s client
func (c *Client) GetDynamicClient() (dynamic.Interface, error) {
	if c.dynamicClient != nil {
		return c.dynamicClient, nil
	}

	c.logger.V(3).Infof("creating new dynamic client")
	var err error
	c.dynamicClient, err = dynamic.NewForConfig(c.Config)
	return c.dynamicClient, err
}

func (c *Client) GetRestMapper() (meta.RESTMapper, error) {
	if c.restMapper != nil {
		return c.restMapper, nil
	}

	// re-use kubectl cache
	host := c.Config.Host
	host = strings.ReplaceAll(host, "https://", "")
	host = strings.ReplaceAll(host, "-", "_")
	host = strings.ReplaceAll(host, ":", "_")
	cacheDir := os.ExpandEnv("$HOME/.kube/cache/discovery/" + host)
	timeout := properties.Duration(240*time.Minute, "kubernetes.cache.timeout")
	c.logger.V(3).Infof("creating new rest mapper with cache dir: %s and timeout: %s", cacheDir, timeout)
	cache, err := disk.NewCachedDiscoveryClientForConfig(
		c.Config,
		cacheDir,
		"",
		timeout,
	)
	if err != nil {
		return nil, err
	}
	c.restMapper = restmapper.NewDeferredDiscoveryRESTMapper(cache)
	return c.restMapper, err
}

func (c *Client) ExecutePodf(
	ctx context.Context,
	namespace, pod, container string,
	command ...string,
) (string, string, error) {
	const tty = false
	req := c.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec").
		Param("container", container).
		Param("stdin", fmt.Sprintf("%t", false)).
		Param("stdout", fmt.Sprintf("%t", true)).
		Param("stderr", fmt.Sprintf("%t", true)).
		Param("tty", fmt.Sprintf("%t", tty))

	for _, c := range command {
		req.Param("command", c)
	}

	exec, err := remotecommand.NewSPDYExecutor(c.Config, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("ExecutePodf: Failed to get SPDY Executor: %v", err)
	}
	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  nil,
		Stdout: &stdout,
		Stderr: &stderr,
		Tty:    tty,
	})

	_stdout := safeString(&stdout)
	_stderr := safeString(&stderr)

	if err != nil {
		return "", "", fmt.Errorf("failed to execute command: %v, stdout=%s stderr=%s", err, _stdout, _stderr)
	}

	return _stdout, _stderr, nil
}

func (c *Client) GetPodLogs(ctx context.Context, namespace, podName, container string) (io.ReadCloser, error) {
	podLogOptions := v1.PodLogOptions{}
	if container != "" {
		podLogOptions.Container = container
	}

	req := c.CoreV1().Pods(namespace).GetLogs(podName, &podLogOptions)
	podLogs, err := req.Stream(ctx)
	if err != nil {
		return nil, err
	}

	return podLogs, nil
}

// WaitForPod waits for a pod to be in the specified phase, or returns an
// error if the timeout is exceeded
func (c *Client) WaitForPod(
	ctx context.Context,
	namespace, name string,
	timeout time.Duration,
	phases ...v1.PodPhase,
) error {
	start := time.Now()
	if len(phases) == 0 {
		phases = []v1.PodPhase{v1.PodRunning}
	}

	pods := c.CoreV1().Pods(namespace)
	for {
		pod, err := pods.Get(ctx, name, metav1.GetOptions{})
		if start.Add(timeout).Before(time.Now()) {
			return fmt.Errorf("timeout exceeded waiting for %s is %s, error: %v", name, pod.Status.Phase, err)
		}

		if pod == nil || pod.Status.Phase == v1.PodPending {
			time.Sleep(5 * time.Second)
			continue
		}
		if pod.Status.Phase == v1.PodFailed {
			return nil
		}

		for _, phase := range phases {
			if pod.Status.Phase == phase {
				return nil
			}
		}
	}
}

// WaitForJob waits for a job to complete or fail within the specified timeout.
// Returns nil if the job completes successfully, or an error if the job fails or times out.
func (c *Client) WaitForJob(
	ctx context.Context,
	namespace, name string,
	timeout time.Duration,
) error {
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	jobs := c.BatchV1().Jobs(namespace)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-timeoutTimer.C:
			job, _ := jobs.Get(ctx, name, metav1.GetOptions{})
			if job != nil {
				return fmt.Errorf("timeout exceeded waiting for job %s: active=%d, succeeded=%d, failed=%d",
					name, job.Status.Active, job.Status.Succeeded, job.Status.Failed)
			}
			return fmt.Errorf("timeout exceeded waiting for job %s", name)

		case <-ticker.C:
			job, err := jobs.Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				if apiErrors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("failed to get job %s: %w", name, err)
			}

			for _, condition := range job.Status.Conditions {
				if condition.Type == "Complete" && condition.Status == "True" {
					return nil
				}
				if condition.Type == "Failed" && condition.Status == "True" {
					return fmt.Errorf("job %s failed: %s", name, condition.Message)
				}
			}
		}
	}
}

func (c *Client) StreamLogsV2(
	ctx context.Context,
	namespace, name string,
	timeout time.Duration,
	containerNames ...string,
) error {
	podsClient := c.CoreV1().Pods(namespace)
	pod, err := podsClient.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if err := c.WaitForContainerStart(ctx, namespace, name, 120*time.Second, containerNames...); err != nil {
		return err
	}

	var wg sync.WaitGroup
	containers := list.New()

	for _, container := range append(pod.Spec.Containers, pod.Spec.InitContainers...) {
		if len(containerNames) == 0 || lo.Contains(containerNames, container.Name) {
			containers.PushBack(container)
		}
	}

	// Loop over container list.
	for element := containers.Front(); element != nil; element = element.Next() {
		container := element.Value.(v1.Container)
		logs := podsClient.GetLogs(pod.Name, &v1.PodLogOptions{
			Container: container.Name,
		})

		prefix := pod.Name
		if len(pod.Spec.Containers) > 1 {
			prefix += "/" + container.Name
		}

		podLogs, err := logs.Stream(ctx)
		if err != nil {
			containers.PushBack(container)
			logger.Tracef("failed to begin streaming %s/%s: %s", pod.Name, container.Name, err)
			time.Sleep(500 * time.Millisecond)
			continue
		}

		wg.Add(1)

		go func() {
			defer podLogs.Close()
			defer wg.Done()

			scanner := bufio.NewScanner(podLogs)
			for scanner.Scan() {
				incoming := scanner.Bytes()
				buffer := make([]byte, len(incoming))
				copy(buffer, incoming)
				fmt.Printf("\x1b[38;5;244m[%s]\x1b[0m %s\n", prefix, string(buffer))
			}
		}()
	}

	wg.Wait()

	if err = c.WaitForPod(ctx, namespace, name, timeout, v1.PodSucceeded); err != nil {
		return err
	}

	pod, err = podsClient.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if pod.Status.Phase == v1.PodSucceeded {
		return nil
	}

	return fmt.Errorf("pod did not finish successfully %s - %s", pod.Status.Phase, pod.Status.Message)
}

// WaitForContainerStart waits for the specified containers to be started (or any container if no names are specified) - returns an error if the timeout is exceeded
func (c *Client) WaitForContainerStart(
	ctx context.Context,
	namespace, name string,
	timeout time.Duration,
	containerNames ...string,
) error {
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	podsClient := c.CoreV1().Pods(namespace)
	for {
		select {
		case <-timeoutTimer.C:
			return fmt.Errorf("timeout exceeded waiting for %s", name)

		case <-ctx.Done():
			return ctx.Err()

		default:
			pod, err := podsClient.Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				if apiErrors.IsNotFound(err) {
					time.Sleep(time.Second)
					continue
				}

				return err
			}

			for _, container := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
				if len(containerNames) > 0 && !lo.Contains(containerNames, container.Name) {
					continue
				}

				if container.State.Running != nil || container.State.Terminated != nil {
					return nil
				}
			}

			time.Sleep(time.Second)
		}
	}
}

func (c *Client) ExpandNamespaces(ctx context.Context, namespace string) ([]string, error) {
	namespaces := []string{}
	if namespace == "*" || namespace == "" {
		return []string{""}, nil
	}

	if !types.IsMatchItem(namespace) {
		return []string{namespace}, nil
	}

	resources, err := c.QueryResources(ctx, types.ResourceSelector{Name: namespace}.Type("Namesapce"))
	if err != nil {
		return nil, err
	}

	for _, resource := range resources {
		namespaces = append(namespaces, resource.GetName())
	}

	return namespaces, nil
}

func (c *Client) QueryResources(ctx context.Context, selector types.ResourceSelector) ([]unstructured.Unstructured, error) {
	timer := timer.NewTimer()

	var resources []unstructured.Unstructured
	for _, kind := range selector.Types {
		if strings.ToLower(kind) == "namespace" && selector.IsMetadataOnly() {
			if name, ok := selector.ToGetOptions(); ok {
				return []unstructured.Unstructured{{
					Object: map[string]any{
						"apiVersion:": "v1",
						"kind":        "Namespace",
						"metadata": map[string]any{
							"name": name,
						},
					},
				}}, nil
			}
		}

		client, rm, err := c.GetClientByKind(strings.TrimPrefix(kind, "Kubernetes::"))
		if err != nil {
			return nil, fmt.Errorf("failed to get client for %s: %w", kind, err)
		}

		isClusterScoped := rm.Scope.Name() == meta.RESTScopeNameRoot

		var namespaces []string
		if isClusterScoped {
			namespaces = []string{""}
		} else {
			namespaces, err = c.ExpandNamespaces(ctx, selector.Namespace)
			if apiErrors.IsNotFound(err) {
				continue
			} else if err != nil {
				return nil, fmt.Errorf("failed to expand namespaces for %s: %w", kind, err)
			}
		}

		for _, namespace := range namespaces {
			cc := client.Namespace(namespace)
			if isClusterScoped {
				cc = client
			}

			if name, ok := selector.ToGetOptions(); ok && !types.IsMatchItem(name) {
				resource, err := cc.Get(ctx, name, metav1.GetOptions{})
				if apiErrors.IsNotFound(err) {
					continue
				} else if err != nil {
					return nil, fmt.Errorf("failed to get resource %s/%s: %w", namespace, name, err)
				}

				resources = append(resources, *resource)
				continue
			}

			list, full := selector.ToListOptions()
			resourceList, err := cc.List(ctx, list)
			if err != nil {
				return nil, fmt.Errorf("failed to list resources %s/%s: %w", namespace, selector.Name, err)
			}

			if full {
				resources = append(resources, resourceList.Items...)
				continue
			}

			for _, resource := range resourceList.Items {
				if selector.Matches(&types.UnstructuredResource{Unstructured: &resource}) {
					resources = append(resources, resource)
				}
			}
		}
	}

	c.logger.Debugf("%s => count=%d duration=%s", selector, len(resources), timer)
	return resources, nil
}

type Resource struct {
	unstructured.Unstructured
	health.HealthStatus
}

func (r *Resource) IsHealthy() bool {
	return r.Health == health.HealthHealthy
}

func (r Resource) Pretty() api.Text {
	t := api.Text{}
	t = t.Append(r.GetKind(), "text-bold").Append("/", "text-muted").Append(r.GetNamespace()).Append("/", "text-muted").Append(r.GetName())
	t = t.Space()
	if r.IsHealthy() {
		t = t.Append(icons.Pass, "text-green-500")
	} else if r.Health == health.HealthWarning {
		t = t.Append(icons.Warning, "text-yellow-500")
	} else {
		t = t.Append(icons.Fail, "text-red-500")
	}
	if r.Message != "" {
		t = t.Space().Append(r.Message)
	}
	return t
}

type Resources []Resource

func (r Resources) AsUnstructured() []unstructured.Unstructured {
	out := []unstructured.Unstructured{}
	for _, res := range r {
		out = append(out, res.Unstructured)
	}
	return out
}

func (r Resources) Pretty() api.Text {
	t := api.Text{}
	for i, res := range r {
		if i > 0 {
			t = t.NewLine()
		}
		t = t.Append(res.Pretty())
	}
	return t
}

func NewResources(objs ...unstructured.Unstructured) Resources {
	resources := Resources{}
	for _, obj := range objs {
		healthy, _ := health.GetResourceHealth(&obj, health.DefaultOverrides)
		if healthy == nil {
			healthy = &health.HealthStatus{
				Health: health.HealthUnknown,
			}
		}
		resources = append(resources, Resource{Unstructured: obj, HealthStatus: *healthy})
	}
	return resources
}

func (c *Client) GetResource(ctx context.Context, kind, namespace, name string) (*Resource, error) {

	resource, err := c.Get(ctx, kind, namespace, name)
	if errors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil || resource == nil {
		return nil, err
	}

	healthy, err := health.GetResourceHealth(resource, health.DefaultOverrides)
	if healthy == nil {
		healthy = &health.HealthStatus{
			Health: health.HealthUnknown,
		}
	}

	return &Resource{
		Unstructured: *resource,
		HealthStatus: *healthy,
	}, err
}

func (c *Client) WaitForReady(ctx context.Context, kind, namespace, name string, timeout ...time.Duration) (*Resource, error) {
	var timeoutDuration time.Duration
	if len(timeout) > 0 {
		timeoutDuration = timeout[0]
	} else {
		timeoutDuration = time.Minute
	}

	start := time.Now()

	ticker := time.NewTicker(time.Second * 1)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case <-ticker.C:
			r, _ := c.GetResource(ctx, kind, namespace, name)
			if r != nil && r.IsHealthy() {
				return r, nil
			}

			if start.Add(timeoutDuration).Before(time.Now()) {

				if r != nil {
					return nil, fmt.Errorf("timeout exceeded waiting for %s", r.Pretty().String())
				} else {
					return nil, fmt.Errorf("timeout exceeded waiting for %s/%s/%s", kind, namespace, name)
				}

			}
		}
	}
}

func (c *Client) WaitFor(ctx context.Context, kind, namespace, name string, condition func(*unstructured.Unstructured) bool, timeout time.Duration) (*unstructured.Unstructured, error) {
	client, _, err := c.GetClientByKind(kind)
	if err != nil {
		return nil, err
	}

	ticker := time.NewTicker(time.Second * 1)
	defer ticker.Stop()

	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeoutTimer.C:
			return nil, fmt.Errorf("timeout exceeded waiting for %s/%s/%s", kind, namespace, name)
		case <-ticker.C:
			resource, err := client.Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				if apiErrors.IsNotFound(err) {
					continue
				}
				return nil, err
			}

			if condition(resource) {
				return resource, nil
			}
		}
	}
}

func (c *Client) ApplyFile(ctx context.Context, files ...string) (Resources, error) {
	all := Resources{}
	for _, file := range files {
		manifest, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		out, err := c.Apply(ctx, string(manifest))
		if err != nil {
			return nil, err
		}
		all = append(all, out...)
	}
	return all, nil
}

func (c *Client) Apply(ctx context.Context, manifest string) (Resources, error) {
	out := []unstructured.Unstructured{}
	in, err := GetUnstructuredObjects([]byte(manifest))
	if err != nil {
		return nil, err
	}

	for _, o := range in {
		if !strings.HasPrefix(o.GetKind(), "Cluster") {
			o.SetNamespace(c.defaultNamespace)
		}
		c.logger.Infof("Applying %s", NewResources(o).Pretty().ANSI())
		dynClient, err := c.GetClientByGroupVersionKind(ctx, o.GroupVersionKind().Group, o.GroupVersionKind().Version, o.GetKind())
		if err != nil {
			return nil, fmt.Errorf("Failed to get client for %s/%s/%s", o.GroupVersionKind().Group, o.GroupVersionKind().Version, o.GetKind())
		}
		saved, err := dynClient.Namespace(o.GetNamespace()).Apply(ctx, o.GetName(), &o, metav1.ApplyOptions{
			FieldManager: "flanksource-commons",
		})
		if err != nil {
			return nil, fmt.Errorf("Failed to apply %s/%s/%s: %w", o.GroupVersionKind().Group, o.GroupVersionKind().Version, o.GetKind(), err)
		}
		out = append(out, *saved)
	}

	return NewResources(out...), nil
}

func (c *Client) GetPodIP(ctx context.Context, namespace, selector string) (string, error) {
	pods, err := c.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return "", err
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods found for selector %s in namespace %s", selector, namespace)
	}
	messages := make(map[string]string)
	for _, pod := range pods.Items {
		if ok, msg := health.IsPodReady(&pod); ok {
			return pod.Status.PodIP, nil
		} else {
			messages[pod.Name] = msg
		}
	}
	return "", fmt.Errorf("no ready pods found for selector %s in namespace %s: %v", selector, namespace, messages)
}

func safeString(buf *bytes.Buffer) string {
	if buf == nil || buf.Len() == 0 {
		return ""
	}
	return buf.String()
}
