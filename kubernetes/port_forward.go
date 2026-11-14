package kubernetes

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// PortForwardOptions configures port forwarding to a Kubernetes resource.
// Either Name or LabelSelector must be provided to identify the target resource;
// Kind is one of WorkloadKinds.
type PortForwardOptions struct {
	Namespace     string `json:"namespace,omitempty"`
	Name          string `json:"name,omitempty"`
	LabelSelector string `json:"labelSelector,omitempty"`
	RemotePort    int    `json:"remotePort,omitempty"`
	Kind          string `json:"kind"`
}

func (o PortForwardOptions) workloadTarget() WorkloadTarget {
	port := ""
	if o.RemotePort > 0 {
		port = strconv.Itoa(o.RemotePort)
	}
	return WorkloadTarget{Namespace: o.Namespace, Kind: o.Kind, Name: o.Name, LabelSelector: o.LabelSelector, Port: port}
}

// PortForward establishes an SPDY port-forward tunnel to the workload identified by opts,
// resolving it to a Ready pod with ResolvePodPort. It returns the allocated local port, a
// stop channel that tears the tunnel down when closed, and any error.
func PortForward(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (int, chan struct{}, error) {
	session, err := establishManagedPortForward(ctx, k8s, restConfig, opts)
	if err != nil {
		return 0, nil, err
	}
	return session.localPort, session.stop, nil
}

func establishManagedPortForward(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (*forwardSession, error) {
	target, err := ResolvePodPort(ctx, k8s, opts.workloadTarget())
	if err != nil {
		return nil, err
	}
	return portForwardToPod(ctx, restConfig, target)
}

// PortForwardPod sets up port forwarding to a pod matching the given name or label selector.
// Returns the local port, a stop channel to close when done, and any error.
func PortForwardPod(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (int, chan struct{}, error) {
	opts.Kind = "pod"
	return PortForward(ctx, k8s, restConfig, opts)
}

// PortForwardService sets up port forwarding to a pod backing the specified service.
// The service is found by Name or LabelSelector. Returns the local port, a stop channel, and any error.
func PortForwardService(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (int, chan struct{}, error) {
	opts.Kind = "service"
	return PortForward(ctx, k8s, restConfig, opts)
}

// PortForwardDeployment sets up port forwarding to a pod managed by the specified deployment.
// Without a Name the LabelSelector selects the pods. Returns the local port, a stop channel, and any error.
func PortForwardDeployment(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (int, chan struct{}, error) {
	opts.Kind = "deployment"
	return PortForward(ctx, k8s, restConfig, opts)
}

// serviceTargetPort resolves requested (a Service port number or name, or empty
// for the first Service port) to the container port on pod it targets.
func serviceTargetPort(service *corev1.Service, pod *corev1.Pod, requested string) (int, error) {
	if len(service.Spec.Ports) == 0 {
		return 0, fmt.Errorf("service %s has no ports", service.Name)
	}
	servicePort := &service.Spec.Ports[0]
	if requested != "" {
		servicePort = nil
		for i := range service.Spec.Ports {
			if strconv.Itoa(int(service.Spec.Ports[i].Port)) == requested || service.Spec.Ports[i].Name == requested {
				servicePort = &service.Spec.Ports[i]
				break
			}
		}
		if servicePort == nil {
			return 0, fmt.Errorf("service %s does not expose port %s", service.Name, requested)
		}
	}

	switch servicePort.TargetPort.Type {
	case intstr.Int:
		if servicePort.TargetPort.IntValue() > 0 {
			return servicePort.TargetPort.IntValue(), nil
		}
		return int(servicePort.Port), nil
	case intstr.String:
		return containerPort(pod, servicePort.TargetPort.StrVal)
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
func portForwardToPod(ctx context.Context, restConfig *rest.Config, target PodPort) (*forwardSession, error) {
	if restConfig == nil {
		return nil, fmt.Errorf("kubernetes REST config is required")
	}
	serverURL, err := url.Parse(restConfig.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to parse server URL: %w", err)
	}
	serverURL.Path = fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", target.Namespace, target.Pod)

	transport, upgrader, err := spdy.RoundTripperFor(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create round tripper: %w", err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, serverURL)

	stopChan := make(chan struct{}, 1)
	readyChan := make(chan struct{})

	// Let client-go bind port zero and report the selected port after readiness;
	// this avoids releasing a pre-selected port before the forwarder binds it.
	ports := []string{fmt.Sprintf("0:%d", target.Port)}
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
