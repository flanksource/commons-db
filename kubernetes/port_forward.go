package kubernetes

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// PortForwardOptions configures port forwarding to a Kubernetes resource.
// Either Name or LabelSelector must be provided to identify the target resource.
type PortForwardOptions struct {
	Namespace     string `json:"namespace,omitempty"`
	Name          string `json:"name,omitempty"`
	LabelSelector string `json:"labelSelector,omitempty"`
	RemotePort    int    `json:"remotePort,omitempty"`
	Kind          string `json:"kind"`
}

// PortForward establishes an SPDY port-forward tunnel to the workload identified by opts,
// dispatching on Kind. It returns the allocated local port, a stop channel that tears the
// tunnel down when closed, and any error.
func PortForward(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (int, chan struct{}, error) {
	session, err := establishManagedPortForward(ctx, k8s, restConfig, opts)
	if err != nil {
		return 0, nil, err
	}
	return session.localPort, session.stop, nil
}

func establishManagedPortForward(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (*forwardSession, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	switch opts.Kind {
	case "pod":
		return portForwardPod(ctx, k8s, restConfig, opts)
	case "deployment":
		return portForwardDeployment(ctx, k8s, restConfig, opts)
	case "service":
		return portForwardService(ctx, k8s, restConfig, opts)
	}

	// This never happens since Kind is validated in opts.validate()
	return nil, fmt.Errorf("invalid kind:%s", opts.Kind)
}

// PortForwardPod sets up port forwarding to a pod matching the given name or label selector.
// Returns the local port, a stop channel to close when done, and any error.
func PortForwardPod(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (int, chan struct{}, error) {
	session, err := portForwardPod(ctx, k8s, restConfig, opts)
	if err != nil {
		return 0, nil, err
	}
	return session.localPort, session.stop, nil
}

func portForwardPod(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (*forwardSession, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}

	var pod *corev1.Pod
	if opts.Name != "" {
		p, err := k8s.CoreV1().Pods(opts.Namespace).Get(ctx, opts.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("pod %s not found: %w", opts.Name, err)
		}
		pod = p
	} else {
		pods, err := k8s.CoreV1().Pods(opts.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: opts.LabelSelector,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list pods: %w", err)
		}
		pod, err = selectReadyPod(pods.Items)
		if err != nil {
			return nil, fmt.Errorf("no ready pods found matching selector %s: %w", opts.LabelSelector, err)
		}
	}
	if !podReady(pod) {
		return nil, fmt.Errorf("pod %s is not running and ready", pod.Name)
	}

	remotePort, err := getRemotePort(opts.RemotePort, pod)
	if err != nil {
		return nil, err
	}

	return portForwardToPod(ctx, restConfig, opts.Namespace, pod.Name, remotePort)
}

// PortForwardService sets up port forwarding to a pod backing the specified service.
// The service is found by Name or LabelSelector. Returns the local port, a stop channel, and any error.
func PortForwardService(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (int, chan struct{}, error) {
	session, err := portForwardService(ctx, k8s, restConfig, opts)
	if err != nil {
		return 0, nil, err
	}
	return session.localPort, session.stop, nil
}

func portForwardService(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (*forwardSession, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}

	var svc *corev1.Service

	if opts.Name != "" {
		found, err := k8s.CoreV1().Services(opts.Namespace).Get(ctx, opts.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("service %s not found: %w", opts.Name, err)
		}
		svc = found
	} else {
		services, err := k8s.CoreV1().Services(opts.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: opts.LabelSelector,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list services: %w", err)
		}
		if len(services.Items) == 0 {
			return nil, fmt.Errorf("no services found matching selector %s", opts.LabelSelector)
		}
		sort.Slice(services.Items, func(i, j int) bool { return services.Items[i].Name < services.Items[j].Name })
		svc = &services.Items[0]
	}

	if len(svc.Spec.Selector) == 0 {
		return nil, fmt.Errorf("service %s has no selector", svc.Name)
	}

	pods, err := k8s.CoreV1().Pods(opts.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(svc.Spec.Selector).String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods for service: %w", err)
	}
	pod, err := selectReadyPod(pods.Items)
	if err != nil {
		return nil, fmt.Errorf("no ready pods found for service %s: %w", svc.Name, err)
	}

	remotePort, err := serviceTargetPort(svc, pod, opts.RemotePort)
	if err != nil {
		return nil, err
	}

	return portForwardToPod(ctx, restConfig, opts.Namespace, pod.Name, remotePort)
}

// PortForwardDeployment sets up port forwarding to a pod managed by the specified deployment.
// The deployment is found by Name or LabelSelector. Returns the local port, a stop channel, and any error.
func PortForwardDeployment(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (int, chan struct{}, error) {
	session, err := portForwardDeployment(ctx, k8s, restConfig, opts)
	if err != nil {
		return 0, nil, err
	}
	return session.localPort, session.stop, nil
}

func portForwardDeployment(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (*forwardSession, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}

	var selector labels.Selector

	if opts.Name != "" {
		deployment, err := k8s.AppsV1().Deployments(opts.Namespace).Get(ctx, opts.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("deployment %s not found: %w", opts.Name, err)
		}
		selector, err = metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
		if err != nil {
			return nil, fmt.Errorf("invalid deployment selector: %w", err)
		}
	} else {
		deployments, err := k8s.AppsV1().Deployments(opts.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: opts.LabelSelector,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list deployments: %w", err)
		}
		if len(deployments.Items) == 0 {
			return nil, fmt.Errorf("no deployments found matching selector %s", opts.LabelSelector)
		}
		sort.Slice(deployments.Items, func(i, j int) bool { return deployments.Items[i].Name < deployments.Items[j].Name })
		selector, err = metav1.LabelSelectorAsSelector(deployments.Items[0].Spec.Selector)
		if err != nil {
			return nil, fmt.Errorf("invalid deployment selector: %w", err)
		}
	}

	pods, err := k8s.CoreV1().Pods(opts.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods for deployment: %w", err)
	}
	pod, err := selectReadyPod(pods.Items)
	if err != nil {
		return nil, fmt.Errorf("no ready pods found for deployment: %w", err)
	}

	remotePort, err := getRemotePort(opts.RemotePort, pod)
	if err != nil {
		return nil, err
	}

	return portForwardToPod(ctx, restConfig, opts.Namespace, pod.Name, remotePort)
}

func (o PortForwardOptions) validate() error {
	if !slices.Contains([]string{"pod", "service", "deployment"}, o.Kind) {
		return fmt.Errorf("kind[%s] should be one of pod, service, deployment", o.Kind)
	}
	if o.Namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if o.Name == "" && o.LabelSelector == "" {
		return fmt.Errorf("either Name or LabelSelector must be provided")
	}
	return nil
}

// getRemotePort returns the port to forward to. If remotePort is specified (> 0),
// it returns that. Otherwise, it returns the first container port from the pod.
func getRemotePort(remotePort int, pod *corev1.Pod) (int, error) {
	if remotePort > 0 {
		return remotePort, nil
	}

	for _, container := range pod.Spec.Containers {
		if len(container.Ports) > 0 {
			return int(container.Ports[0].ContainerPort), nil
		}
	}

	return 0, fmt.Errorf("pod %s has no container ports and remotePort was not specified", pod.Name)
}

func serviceTargetPort(service *corev1.Service, pod *corev1.Pod, requested int) (int, error) {
	if len(service.Spec.Ports) == 0 {
		return 0, fmt.Errorf("service %s has no ports", service.Name)
	}
	servicePort := &service.Spec.Ports[0]
	if requested > 0 {
		servicePort = nil
		for i := range service.Spec.Ports {
			if int(service.Spec.Ports[i].Port) == requested {
				servicePort = &service.Spec.Ports[i]
				break
			}
		}
		if servicePort == nil {
			return 0, fmt.Errorf("service %s does not expose port %d", service.Name, requested)
		}
	}

	switch servicePort.TargetPort.Type {
	case intstr.Int:
		if servicePort.TargetPort.IntValue() > 0 {
			return servicePort.TargetPort.IntValue(), nil
		}
		return int(servicePort.Port), nil
	case intstr.String:
		name := servicePort.TargetPort.StrVal
		for _, container := range pod.Spec.Containers {
			for _, port := range container.Ports {
				if port.Name == name {
					return int(port.ContainerPort), nil
				}
			}
		}
		return 0, fmt.Errorf("pod %s has no container port named %q", pod.Name, name)
	default:
		return 0, fmt.Errorf("service %s has an unsupported target port", service.Name)
	}
}

func selectReadyPod(pods []corev1.Pod) (*corev1.Pod, error) {
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	for i := range pods {
		if podReady(&pods[i]) {
			return &pods[i], nil
		}
	}
	return nil, fmt.Errorf("no running, ready pod found")
}

func podReady(pod *corev1.Pod) bool {
	if pod == nil || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// portForwardToPod establishes port forwarding to a specific pod over an SPDY tunnel.
func portForwardToPod(ctx context.Context, restConfig *rest.Config, namespace, podName string, remotePort int) (*forwardSession, error) {
	if restConfig == nil {
		return nil, fmt.Errorf("kubernetes REST config is required")
	}
	serverURL, err := url.Parse(restConfig.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to parse server URL: %w", err)
	}
	serverURL.Path = fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, podName)

	transport, upgrader, err := spdy.RoundTripperFor(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create round tripper: %w", err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, serverURL)

	stopChan := make(chan struct{}, 1)
	readyChan := make(chan struct{})

	// Let client-go bind port zero and report the selected port after readiness;
	// this avoids releasing a pre-selected port before the forwarder binds it.
	ports := []string{fmt.Sprintf("0:%d", remotePort)}
	pf, err := portforward.New(dialer, ports, stopChan, readyChan, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create port forwarder: %w", err)
	}

	errChan := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := pf.ForwardPorts(); err != nil {
			errChan <- err
		}
	}()

	select {
	case <-readyChan:
		ports, err := pf.GetPorts()
		if err != nil || len(ports) == 0 {
			close(stopChan)
			if err == nil {
				err = fmt.Errorf("port forward reported no bound ports")
			}
			return nil, err
		}
		select {
		case err := <-errChan:
			return nil, fmt.Errorf("port forward failed after readiness: %w", err)
		default:
		}
		return &forwardSession{localPort: int(ports[0].Local), stop: stopChan, done: done}, nil
	case err := <-errChan:
		return nil, fmt.Errorf("port forward failed: %w", err)
	case <-ctx.Done():
		close(stopChan)
		return nil, ctx.Err()
	}
}
